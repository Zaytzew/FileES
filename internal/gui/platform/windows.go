//go:build windows

package platform

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	autostartRegKey             = `Software\Microsoft\Windows\CurrentVersion\Run`
	defaultWindowsNotifInterval = 2 * time.Second
	// Windows PowerShell 5.1 inherits an OEM console encoding even when stdout
	// is a pipe. Go receives those bytes verbatim and interprets them as UTF-8,
	// corrupting perfectly valid paths such as ŁÓDŹ into replacement runes.
	// Force UTF-8 at the one process boundary shared by every picker/dialog
	// that returns text to FileES. UTF8Encoding(false) avoids a BOM in the
	// first returned field.
	powerShellUTF8OutputPrelude = "[Console]::OutputEncoding=New-Object System.Text.UTF8Encoding($false);"
)

// WindowsBackend implements the desktop boundary for Windows 10+. It delegates
// folder opening to explorer.exe, notifications to PowerShell/WinRT and
// autostart to HKCU. Application dialogs and pickers belong to Wails.
type WindowsBackend struct {
	runner        windowsCommandRunner
	autostart     windowsAutostartStore
	now           func() time.Time
	aumid         string
	notifInterval time.Duration
	notifMu       sync.Mutex
	notifGroups   map[string]windowsNotifGroup
}

type windowsNotifGroup struct {
	lastSent time.Time
}

type windowsCommandRunner interface {
	LookPath(name string) (string, error)
	Start(ctx context.Context, name string, args ...string) error
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type osWindowsCommandRunner struct{}

func (osWindowsCommandRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// createNoWindow (CREATE_NO_WINDOW) tells CreateProcess not to allocate a
// console for the child at all, unlike syscall.SysProcAttr.HideWindow (which
// allocates one with STARTF_USESHOWWINDOW/SW_HIDE). Not exported by the
// standard syscall package on Windows, so it is duplicated here.
const createNoWindow = 0x08000000

// hideConsoleWindow suppresses the console window Windows would otherwise
// briefly flash when spawning a console subprocess (e.g. powershell.exe) from
// a GUI-subsystem process. Passing -WindowStyle Hidden to PowerShell is not
// enough on its own: CreateProcess still allocates and shows a console window
// before PowerShell gets a chance to hide it.
//
// HideWindow (STARTF_USESHOWWINDOW + SW_HIDE) looks like the obvious fix, but
// Win32 documents that the *first* ShowWindow call made by a process is
// overridden by the show-state CreateProcess passed in, regardless of what
// the caller explicitly requests. Our prompt/confirm/info/reservation
// dialogs call Form.ShowDialog() as literally the first window the spawned
// powershell.exe ever shows, so HideWindow silently forced every one of
// them to open minimized/hidden instead of focused - confirmed live on a
// real Windows 11 session (see SESSION_HANDOFF.md, Windows client bring-up).
// CREATE_NO_WINDOW avoids the console without touching that show-state
// inheritance, so windows created later by the script are unaffected.
func hideConsoleWindow(cmd *exec.Cmd) {
	if !strings.EqualFold(filepath.Base(cmd.Path), "powershell.exe") {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func (osWindowsCommandRunner) Start(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsoleWindow(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (osWindowsCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsoleWindow(cmd)
	return cmd.Run()
}

func (osWindowsCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	prepared := powerShellUTF8OutputArgs(name, args)
	cmd := exec.CommandContext(ctx, name, prepared...)
	hideConsoleWindow(cmd)
	out, err := cmd.Output()
	if err == nil && strings.EqualFold(filepath.Base(name), "powershell.exe") && !utf8.Valid(out) {
		return nil, errors.New("PowerShell returned output outside UTF-8")
	}
	return out, err
}

func powerShellUTF8OutputArgs(name string, args []string) []string {
	if !strings.EqualFold(filepath.Base(name), "powershell.exe") {
		return args
	}
	prepared := append([]string(nil), args...)
	for i := 0; i+1 < len(prepared); i++ {
		if strings.EqualFold(prepared[i], "-Command") {
			prepared[i+1] = powerShellUTF8OutputPrelude + prepared[i+1]
			return prepared
		}
	}
	return prepared
}

// WindowsOptions contains integration identity supplied by the composition
// root. AUMID must match the FileES Start Menu shortcut installed by packaging.
// Without it notifications report FailureUnavailable instead of impersonating
// another application.
type WindowsOptions struct {
	AUMID string
}

func NewWindowsBackend(options WindowsOptions) *WindowsBackend {
	return newWindowsBackend(osWindowsCommandRunner{}, registryAutostartStore{}, time.Now, options.AUMID)
}

func newWindowsBackend(runner windowsCommandRunner, autostart windowsAutostartStore, now func() time.Time, aumid string) *WindowsBackend {
	return &WindowsBackend{
		runner:        runner,
		autostart:     autostart,
		now:           now,
		aumid:         strings.TrimSpace(aumid),
		notifInterval: defaultWindowsNotifInterval,
		notifGroups:   make(map[string]windowsNotifGroup),
	}
}

func (b *WindowsBackend) OpenFolder(ctx context.Context, path string) error {
	if err := requireAbsolutePath(path); err != nil {
		return NewOperationalFailure("open_folder", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	command, err := b.runner.LookPath("explorer.exe")
	if err != nil {
		return NewUnavailable("open_folder", err)
	}
	if err := b.runner.Start(ctx, command, filepath.Clean(path)); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return NewOperationalFailure("open_folder", err)
	}
	return nil
}

func (b *WindowsBackend) Notify(ctx context.Context, notification Notification) error {
	if strings.TrimSpace(notification.Title) == "" {
		return NewOperationalFailure("notifications", errors.New("title is required"))
	}
	if b.aumid == "" {
		return NewUnavailable("notifications", errors.New("FileES AUMID is not configured"))
	}
	if err := validateWindowsAUMID(b.aumid); err != nil {
		return NewOperationalFailure("notifications", err)
	}
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return NewUnavailable("notifications", err)
	}
	groupKey := notification.Group
	if groupKey == "" {
		groupKey = notification.ID
	}
	if !b.reserveNotification(groupKey) {
		return nil
	}
	script := buildToastScript(notification, windowsToastTag(groupKey), b.aumid)
	if err := b.runner.Run(ctx, command, "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script); err != nil {
		b.releaseNotification(groupKey)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return NewOperationalFailure("notifications", err)
	}
	return nil
}

func (b *WindowsBackend) reserveNotification(key string) bool {
	if key == "" {
		return true
	}
	b.notifMu.Lock()
	defer b.notifMu.Unlock()
	now := b.now()
	group := b.notifGroups[key]
	if !group.lastSent.IsZero() && now.Sub(group.lastSent) < b.notifInterval {
		return false
	}
	group.lastSent = now
	b.notifGroups[key] = group
	return true
}

func (b *WindowsBackend) releaseNotification(key string) {
	if key == "" {
		return
	}
	b.notifMu.Lock()
	b.notifGroups[key] = windowsNotifGroup{}
	b.notifMu.Unlock()
}

// buildToastScript returns a PowerShell one-liner that shows a Windows.UI.Notifications
// toast. The Tag field enables notification replacement for repeated events in the same group.
func buildToastScript(n Notification, tag, aumid string) string {
	var sb strings.Builder
	sb.WriteString("$ErrorActionPreference='Stop';")
	sb.WriteString("[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]|Out-Null;")
	sb.WriteString("[Windows.Data.Xml.Dom.XmlDocument,Windows.Data.Xml.Dom.XmlDocument,ContentType=WindowsRuntime]|Out-Null;")
	sb.WriteString("$x=New-Object Windows.Data.Xml.Dom.XmlDocument;")
	sb.WriteString("$x.LoadXml('<toast><visual><binding template=\"ToastGeneric\"><text></text><text></text></binding></visual></toast>');")
	sb.WriteString("$titleNode=$x.SelectSingleNode('/toast/visual/binding/text[1]');")
	sb.WriteString("$bodyNode=$x.SelectSingleNode('/toast/visual/binding/text[2]');")
	sb.WriteString("[void]$titleNode.AppendChild($x.CreateTextNode(" + psString(n.Title) + "));")
	sb.WriteString("[void]$bodyNode.AppendChild($x.CreateTextNode(" + psString(n.Body) + "));")
	sb.WriteString("$toast=New-Object Windows.UI.Notifications.ToastNotification($x);")
	if tag != "" {
		sb.WriteString("$toast.Tag=" + psString(tag) + ";")
	}
	sb.WriteString("[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(" + psString(aumid) + ").Show($toast)")
	return sb.String()
}

func windowsToastTag(group string) string {
	if group == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(group))
	return fmt.Sprintf("%x", sum[:8])
}

func validateWindowsAUMID(aumid string) error {
	if len(aumid) > 128 {
		return errors.New("AUMID exceeds 128 characters")
	}
	if strings.ContainsAny(aumid, " \t\r\n") {
		return errors.New("AUMID contains whitespace")
	}
	return nil
}

// psString returns value as a PowerShell single-quoted string literal.
func psString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (b *WindowsBackend) AutostartStatus(ctx context.Context, spec AutostartSpec) (AutostartState, error) {
	if err := ctx.Err(); err != nil {
		return AutostartState{}, err
	}
	if err := validateAutostartID(spec.ID); err != nil {
		return AutostartState{}, NewOperationalFailure("autostart", err)
	}
	value, enabled, err := b.autostart.Value(spec.ID)
	if err != nil {
		return AutostartState{}, NewOperationalFailure("autostart", err)
	}
	if !enabled {
		return AutostartState{Source: autostartRegKey}, nil
	}
	return AutostartState{Enabled: true, Current: value == buildWindowsExecLine(spec), Source: autostartRegKey}, nil
}

func (b *WindowsBackend) SetAutostart(ctx context.Context, spec AutostartSpec, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAutostartID(spec.ID); err != nil {
		return NewOperationalFailure("autostart", err)
	}
	if !enabled {
		if err := b.autostart.Delete(spec.ID); err != nil {
			return NewOperationalFailure("autostart", err)
		}
		return nil
	}
	if strings.TrimSpace(spec.Name) == "" {
		return NewOperationalFailure("autostart", errors.New("autostart name is required"))
	}
	if err := requireAbsolutePath(spec.Executable); err != nil {
		return NewOperationalFailure("autostart", fmt.Errorf("executable: %w", err))
	}
	execLine := buildWindowsExecLine(spec)
	if err := b.autostart.Set(spec.ID, execLine); err != nil {
		return NewOperationalFailure("autostart", err)
	}
	return nil
}

func buildWindowsExecLine(spec AutostartSpec) string {
	args := append([]string{filepath.Clean(spec.Executable)}, spec.Args...)
	return windows.ComposeCommandLine(args)
}

type windowsAutostartStore interface {
	Value(name string) (string, bool, error)
	Set(name, value string) error
	Delete(name string) error
}

type registryAutostartStore struct{}

func (registryAutostartStore) Value(name string) (string, bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, autostartRegKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (registryAutostartStore) Set(name, value string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, autostartRegKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(name, value)
}

func (registryAutostartStore) Delete(name string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, autostartRegKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	err = key.DeleteValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

var _ Backend = (*WindowsBackend)(nil)
