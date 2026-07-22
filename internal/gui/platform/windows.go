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
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	autostartRegKey             = `Software\Microsoft\Windows\CurrentVersion\Run`
	defaultWindowsNotifInterval = 2 * time.Second
)

// WindowsBackend implements the desktop boundary for Windows 10+. It delegates
// folder opening to explorer.exe and file picking/notifications to PowerShell
// to avoid CGO and external tooling dependencies.
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

func (osWindowsCommandRunner) Start(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (osWindowsCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func (osWindowsCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
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

func (b *WindowsBackend) PickFiles(ctx context.Context, request PickFilesRequest) (PickFilesResult, error) {
	if err := requireAbsolutePath(request.Root); err != nil {
		return PickFilesResult{}, NewOperationalFailure("file_picker", fmt.Errorf("root: %w", err))
	}
	initialDir := request.InitialDir
	if initialDir == "" {
		initialDir = request.Root
	}
	if err := requirePathInsideRoot(initialDir, request.Root); err != nil {
		return PickFilesResult{}, NewOperationalFailure("file_picker", fmt.Errorf("initial directory: %w", err))
	}

	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return PickFilesResult{}, NewUnavailable("file_picker", err)
	}
	script := buildPickerScript(request, initialDir)
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PickFilesResult{}, ctxErr
		}
		if commandCancelled(err) {
			return PickFilesResult{Cancelled: true}, nil
		}
		return PickFilesResult{}, NewOperationalFailure("file_picker", err)
	}

	paths := splitPickerOutput(string(output))
	if len(paths) == 0 {
		return PickFilesResult{Cancelled: true}, nil
	}
	if !request.AllowMultiple && len(paths) > 1 {
		paths = paths[:1]
	}
	for i, path := range paths {
		if err := requirePathInsideRoot(path, request.Root); err != nil {
			return PickFilesResult{}, NewOperationalFailure("file_picker", fmt.Errorf("selected path: %w", err))
		}
		paths[i] = filepath.Clean(path)
	}
	return PickFilesResult{Paths: paths}, nil
}

func (b *WindowsBackend) PickFolder(ctx context.Context, request PickFolderRequest) (PickFolderResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return PickFolderResult{}, NewUnavailable("folder_picker", err)
	}
	initialDir := request.InitialDir
	if initialDir != "" {
		if err := requireAbsolutePath(initialDir); err != nil {
			return PickFolderResult{}, NewOperationalFailure("folder_picker", err)
		}
	}
	script := "Add-Type -AssemblyName System.Windows.Forms;$d=New-Object System.Windows.Forms.FolderBrowserDialog;"
	if request.Title != "" {
		script += "$d.Description=" + psString(request.Title) + ";"
	}
	if initialDir != "" {
		script += "$d.SelectedPath=" + psString(initialDir) + ";"
	}
	script += "if($d.ShowDialog()-eq[System.Windows.Forms.DialogResult]::OK){$d.SelectedPath}else{exit 1}"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PickFolderResult{}, ctxErr
		}
		if commandCancelled(err) {
			return PickFolderResult{Cancelled: true}, nil
		}
		return PickFolderResult{}, NewOperationalFailure("folder_picker", err)
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return PickFolderResult{Cancelled: true}, nil
	}
	if err := requireAbsolutePath(path); err != nil {
		return PickFolderResult{}, NewOperationalFailure("folder_picker", err)
	}
	return PickFolderResult{Path: filepath.Clean(path)}, nil
}

// buildPickerScript returns a PowerShell one-liner that opens a WinForms file
// dialog. On cancel the script exits with code 1 (recognised by commandCancelled).
func buildPickerScript(request PickFilesRequest, initialDir string) string {
	var sb strings.Builder
	sb.WriteString("Add-Type -AssemblyName System.Windows.Forms;")
	sb.WriteString("$d=New-Object System.Windows.Forms.OpenFileDialog;")
	sb.WriteString("$d.InitialDirectory=" + psString(initialDir) + ";")
	if request.Title != "" {
		sb.WriteString("$d.Title=" + psString(request.Title) + ";")
	}
	if request.AllowMultiple {
		sb.WriteString("$d.Multiselect=$true;")
	}
	sb.WriteString("$null=$d.ShowDialog();")
	sb.WriteString("if($d.DialogResult-eq[System.Windows.Forms.DialogResult]::OK){$d.FileNames -join \"`n\"}else{exit 1}")
	return sb.String()
}

func (b *WindowsBackend) PromptText(ctx context.Context, request PromptTextRequest) (PromptTextResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return PromptTextResult{}, NewUnavailable("text_prompt", err)
	}
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", buildPromptScript(request))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PromptTextResult{}, ctxErr
		}
		if commandCancelled(err) {
			return PromptTextResult{Cancelled: true}, nil
		}
		return PromptTextResult{}, NewOperationalFailure("text_prompt", err)
	}
	return PromptTextResult{Value: strings.TrimSpace(string(output))}, nil
}

func (b *WindowsBackend) ShowInfo(ctx context.Context, request InfoRequest) error {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return NewUnavailable("info_dialog", err)
	}
	script := "Add-Type -AssemblyName System.Windows.Forms;[System.Windows.Forms.MessageBox]::Show(" + psString(request.Text) + "," + psString(request.Title) + ",'OK','Information')|Out-Null"
	if err := b.runner.Run(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script); err != nil {
		return NewOperationalFailure("info_dialog", err)
	}
	return nil
}

func (b *WindowsBackend) Confirm(ctx context.Context, request ConfirmRequest) (bool, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return false, NewUnavailable("confirm_dialog", err)
	}
	script := "Add-Type -AssemblyName System.Windows.Forms;$r=[System.Windows.Forms.MessageBox]::Show(" + psString(request.Text) + "," + psString(request.Title) + ",'YesNo','Question');if($r-eq'Yes'){'yes'}else{'no'}"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, NewOperationalFailure("confirm_dialog", err)
	}
	return strings.TrimSpace(string(output)) == "yes", nil
}

func buildPromptScript(request PromptTextRequest) string {
	var sb strings.Builder
	sb.WriteString("Add-Type -AssemblyName System.Windows.Forms;")
	sb.WriteString("$f=New-Object System.Windows.Forms.Form;$f.Width=520;$f.Height=190;$f.StartPosition='CenterScreen';")
	sb.WriteString("$f.Text=" + psString(request.Title) + ";")
	sb.WriteString("$l=New-Object System.Windows.Forms.Label;$l.Left=12;$l.Top=15;$l.Width=480;$l.Text=" + psString(request.Text) + ";$f.Controls.Add($l);")
	sb.WriteString("$t=New-Object System.Windows.Forms.TextBox;$t.Left=12;$t.Top=45;$t.Width=480;")
	if request.Placeholder != "" {
		sb.WriteString("$t.Text=" + psString(request.Placeholder) + ";")
	}
	if request.Secret {
		sb.WriteString("$t.UseSystemPasswordChar=$true;")
	}
	sb.WriteString("$f.Controls.Add($t);$b=New-Object System.Windows.Forms.Button;$b.Text='OK';$b.Left=412;$b.Top=82;$b.DialogResult='OK';$f.AcceptButton=$b;$f.Controls.Add($b);")
	sb.WriteString("if($f.ShowDialog()-eq[System.Windows.Forms.DialogResult]::OK){$t.Text}else{exit 1}")
	return sb.String()
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
	sb.WriteString("[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime]|Out-Null;")
	sb.WriteString("$x=New-Object Windows.Data.Xml.Dom.XmlDocument;")
	sb.WriteString("$x.LoadXml('<toast><visual><binding template=\"ToastGeneric\"><text></text><text></text></binding></visual></toast>');")
	sb.WriteString("$t=$x.GetElementsByTagName('text');")
	sb.WriteString("$t[0].InnerText=" + psString(n.Title) + ";")
	sb.WriteString("$t[1].InnerText=" + psString(n.Body) + ";")
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
