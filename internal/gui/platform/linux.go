//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultNotificationInterval = 2 * time.Second

// LinuxBackend implements the desktop boundary using tools commonly supplied
// by freedesktop environments. Missing optional tools are reported per feature.
type LinuxBackend struct {
	runner     linuxCommandRunner
	configHome func() (string, error)
	now        func() time.Time

	notificationInterval time.Duration
	notificationMu       sync.Mutex
	notificationSeq      uint64
	notificationGroups   map[string]linuxNotificationGroup
}

type linuxNotificationGroup struct {
	id       uint32
	lastSent time.Time
	seq      uint64
}

type linuxCommandRunner interface {
	LookPath(name string) (string, error)
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type osLinuxCommandRunner struct{}

func (osLinuxCommandRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (osLinuxCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// NewLinuxBackend creates the production Linux desktop adapter.
func NewLinuxBackend() *LinuxBackend {
	return newLinuxBackend(osLinuxCommandRunner{}, os.UserConfigDir, time.Now)
}

func newLinuxBackend(runner linuxCommandRunner, configHome func() (string, error), now func() time.Time) *LinuxBackend {
	return &LinuxBackend{
		runner:               runner,
		configHome:           configHome,
		now:                  now,
		notificationInterval: defaultNotificationInterval,
		notificationGroups:   make(map[string]linuxNotificationGroup),
	}
}

func (b *LinuxBackend) OpenFolder(ctx context.Context, path string) error {
	if err := requireAbsolutePath(path); err != nil {
		return NewOperationalFailure("open_folder", err)
	}
	command, err := b.runner.LookPath("xdg-open")
	if err != nil {
		return NewUnavailable("open_folder", err)
	}
	if _, err := b.runner.Output(ctx, command, filepath.Clean(path)); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return NewOperationalFailure("open_folder", err)
	}
	return nil
}

func (b *LinuxBackend) PickFiles(ctx context.Context, request PickFilesRequest) (PickFilesResult, error) {
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

	command, args, err := b.pickerCommand(request, initialDir)
	if err != nil {
		return PickFilesResult{}, err
	}
	output, err := b.runner.Output(ctx, command, args...)
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

func (b *LinuxBackend) PickFolder(ctx context.Context, request PickFolderRequest) (PickFolderResult, error) {
	initialDir := request.InitialDir
	if initialDir == "" {
		initialDir, _ = os.UserHomeDir()
	}
	if err := requireAbsolutePath(initialDir); err != nil {
		return PickFolderResult{}, NewOperationalFailure("folder_picker", err)
	}
	var command string
	var args []string
	if candidate, err := b.runner.LookPath("zenity"); err == nil {
		command = candidate
		args = []string{"--file-selection", "--directory", "--filename=" + filepath.Clean(initialDir) + string(filepath.Separator)}
		if request.Title != "" {
			args = append(args, "--title="+request.Title)
		}
	} else if candidate, err := b.runner.LookPath("kdialog"); err == nil {
		command = candidate
		args = []string{"--getexistingdirectory", filepath.Clean(initialDir)}
		if request.Title != "" {
			args = append(args, "--title", request.Title)
		}
	} else {
		return PickFolderResult{}, NewUnavailable("folder_picker", errors.New("neither zenity nor kdialog is installed"))
	}
	output, err := b.runner.Output(ctx, command, args...)
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

func (b *LinuxBackend) PromptText(ctx context.Context, request PromptTextRequest) (PromptTextResult, error) {
	command, err := b.runner.LookPath("zenity")
	if err != nil {
		return PromptTextResult{}, NewUnavailable("text_prompt", errors.New("zenity is not installed"))
	}
	args := []string{"--entry"}
	if request.Title != "" {
		args = append(args, "--title="+request.Title)
	}
	if request.Text != "" {
		args = append(args, "--text="+request.Text)
	}
	if request.Placeholder != "" {
		args = append(args, "--entry-text="+request.Placeholder)
	}
	if request.Secret {
		args = append(args, "--hide-text")
	}
	output, err := b.runner.Output(ctx, command, args...)
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

func (b *LinuxBackend) ShowInfo(ctx context.Context, request InfoRequest) error {
	command, err := b.runner.LookPath("zenity")
	if err != nil {
		return NewUnavailable("info_dialog", errors.New("zenity is not installed"))
	}
	args := []string{"--info", "--title=" + request.Title, "--text=" + request.Text, "--no-wrap"}
	if _, err := b.runner.Output(ctx, command, args...); err != nil && ctx.Err() == nil && !commandCancelled(err) {
		return NewOperationalFailure("info_dialog", err)
	}
	return ctx.Err()
}

func (b *LinuxBackend) Confirm(ctx context.Context, request ConfirmRequest) (bool, error) {
	command, err := b.runner.LookPath("zenity")
	if err != nil {
		return false, NewUnavailable("confirm_dialog", errors.New("zenity is not installed"))
	}
	args := []string{"--question", "--title=" + request.Title, "--text=" + request.Text, "--no-wrap"}
	if request.ConfirmText != "" {
		args = append(args, "--ok-label="+request.ConfirmText)
	}
	if request.CancelText != "" {
		args = append(args, "--cancel-label="+request.CancelText)
	}
	if _, err := b.runner.Output(ctx, command, args...); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		if commandCancelled(err) {
			return false, nil
		}
		return false, NewOperationalFailure("confirm_dialog", err)
	}
	return true, nil
}

// ShowReservations uses Zenity's native list window.  Zenity cannot disable a
// single row action, so ReleaseStatus is rendered explicitly and the action
// layer remains the final authority for whether a selected row can be freed.
// The returned ID is an opaque GUI-local handle, never an SVN lock token.
func (b *LinuxBackend) ShowReservations(ctx context.Context, request ReservationDialogRequest) (ReservationDialogResult, error) {
	command, err := b.runner.LookPath("zenity")
	if err != nil {
		return ReservationDialogResult{}, NewUnavailable("reservation_dialog", errors.New("zenity is not installed"))
	}
	args := []string{
		"--list", "--radiolist", "--title=" + request.Title,
		"--text=" + request.Text, "--width=1200", "--height=560",
		"--column=", "--column=ID", "--column=Serwer", "--column=Kopia robocza", "--column=Plik",
		"--column=Właściciel", "--column=Utworzono", "--column=Zwolnienie",
		"--hide-column=2", "--print-column=2", "--ok-label=Zwolnij",
		"--cancel-label=Zamknij", "--extra-button=Odśwież",
	}
	for _, row := range request.Rows {
		args = append(args, "FALSE", row.ID, row.Server, row.WorkingCopy, row.Path, row.Owner, row.CreatedAt, row.ReleaseStatus)
	}
	output, err := b.runner.Output(ctx, command, args...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ReservationDialogResult{}, ctxErr
		}
		if commandCancelled(err) {
			return ReservationDialogResult{Action: ReservationDialogClose}, nil
		}
		return ReservationDialogResult{}, NewOperationalFailure("reservation_dialog", err)
	}
	selection := strings.TrimSpace(string(output))
	if selection == "Odśwież" {
		return ReservationDialogResult{Action: ReservationDialogRefresh}, nil
	}
	for _, row := range request.Rows {
		if selection == row.ID {
			return ReservationDialogResult{Action: ReservationDialogRelease, RowID: row.ID}, nil
		}
	}
	// Zenity permits confirming an empty selection.  Treat it as a harmless
	// close instead of inventing a release target.
	return ReservationDialogResult{Action: ReservationDialogClose}, nil
}

func (b *LinuxBackend) pickerCommand(request PickFilesRequest, initialDir string) (string, []string, error) {
	if command, err := b.runner.LookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--separator=\n", "--filename=" + filepath.Clean(initialDir) + string(filepath.Separator)}
		if request.AllowMultiple {
			args = append(args, "--multiple")
		}
		if request.Title != "" {
			args = append(args, "--title="+request.Title)
		}
		return command, args, nil
	}
	if command, err := b.runner.LookPath("kdialog"); err == nil {
		args := []string{"--getopenfilename", filepath.Clean(initialDir), "*"}
		if request.AllowMultiple {
			args = append(args, "--multiple", "--separate-output")
		}
		if request.Title != "" {
			args = append(args, "--title", request.Title)
		}
		return command, args, nil
	}
	return "", nil, NewUnavailable("file_picker", errors.New("neither zenity nor kdialog is installed"))
}

func (b *LinuxBackend) Notify(ctx context.Context, notification Notification) error {
	command, err := b.runner.LookPath("notify-send")
	if err != nil {
		return NewUnavailable("notifications", err)
	}
	if strings.TrimSpace(notification.Title) == "" {
		return NewOperationalFailure("notifications", errors.New("title is required"))
	}

	groupKey := notification.Group
	if groupKey == "" {
		groupKey = notification.ID
	}
	group, seq, send := b.reserveNotification(groupKey)
	if !send {
		return nil
	}

	args := []string{
		"--app-name=FileES",
		"--urgency=" + linuxUrgency(notification.Urgency),
		"--print-id",
	}
	if group.id != 0 {
		args = append(args, "--replace-id="+strconv.FormatUint(uint64(group.id), 10))
	}
	args = append(args, notification.Title)
	if notification.Body != "" {
		args = append(args, notification.Body)
	}
	output, err := b.runner.Output(ctx, command, args...)
	if err != nil {
		b.releaseNotification(groupKey, seq)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return NewOperationalFailure("notifications", err)
	}
	id, _ := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 32)
	b.completeNotification(groupKey, seq, uint32(id))
	return nil
}

func (b *LinuxBackend) reserveNotification(key string) (linuxNotificationGroup, uint64, bool) {
	b.notificationMu.Lock()
	defer b.notificationMu.Unlock()
	now := b.now()
	group := b.notificationGroups[key]
	if key != "" && !group.lastSent.IsZero() && now.Sub(group.lastSent) < b.notificationInterval {
		return group, 0, false
	}
	b.notificationSeq++
	group.lastSent = now
	group.seq = b.notificationSeq
	if key != "" {
		b.notificationGroups[key] = group
	}
	return group, group.seq, true
}

func (b *LinuxBackend) completeNotification(key string, seq uint64, id uint32) {
	if key == "" {
		return
	}
	b.notificationMu.Lock()
	group := b.notificationGroups[key]
	if group.seq == seq {
		group.id = id
		b.notificationGroups[key] = group
	}
	b.notificationMu.Unlock()
}

func (b *LinuxBackend) releaseNotification(key string, seq uint64) {
	if key == "" {
		return
	}
	b.notificationMu.Lock()
	group := b.notificationGroups[key]
	if group.seq == seq {
		group.lastSent = time.Time{}
		b.notificationGroups[key] = group
	}
	b.notificationMu.Unlock()
}

func (b *LinuxBackend) AutostartStatus(ctx context.Context, spec AutostartSpec) (AutostartState, error) {
	if err := ctx.Err(); err != nil {
		return AutostartState{}, err
	}
	path, err := b.autostartPath(spec)
	if err != nil {
		return AutostartState{}, NewOperationalFailure("autostart", err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return AutostartState{Source: path}, nil
	}
	if err != nil {
		return AutostartState{}, NewOperationalFailure("autostart", err)
	}
	hidden := desktopBoolean(data, "Hidden")
	current := false
	if !hidden {
		expected, renderErr := renderAutostartDesktop(spec)
		current = renderErr == nil && string(data) == string(expected)
	}
	return AutostartState{Enabled: !hidden, Current: current, Source: path}, nil
}

func (b *LinuxBackend) SetAutostart(ctx context.Context, spec AutostartSpec, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := b.autostartPath(spec)
	if err != nil {
		return NewOperationalFailure("autostart", err)
	}
	var data []byte
	if enabled {
		data, err = renderAutostartDesktop(spec)
	} else {
		data = []byte("[Desktop Entry]\nType=Application\nHidden=true\n")
	}
	if err != nil {
		return NewOperationalFailure("autostart", err)
	}
	if err := atomicWriteFile(path, data, 0o600); err != nil {
		return NewOperationalFailure("autostart", err)
	}
	return nil
}

func (b *LinuxBackend) autostartPath(spec AutostartSpec) (string, error) {
	if err := validateAutostartID(spec.ID); err != nil {
		return "", err
	}
	configHome, err := b.configHome()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(configHome) {
		return "", errors.New("user config directory is not absolute")
	}
	return filepath.Join(configHome, "autostart", spec.ID+".desktop"), nil
}

func renderAutostartDesktop(spec AutostartSpec) ([]byte, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, errors.New("autostart name is required")
	}
	if err := requireAbsolutePath(spec.Executable); err != nil {
		return nil, fmt.Errorf("executable: %w", err)
	}
	execParts := make([]string, 0, 1+len(spec.Args))
	execParts = append(execParts, desktopExecArg(filepath.Clean(spec.Executable)))
	for _, arg := range spec.Args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return nil, errors.New("autostart argument contains a control character")
		}
		execParts = append(execParts, desktopExecArg(arg))
	}
	name := desktopString(spec.Name)
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Version=1.5\n" +
		"Name=" + name + "\n" +
		"Exec=" + strings.Join(execParts, " ") + "\n" +
		"TryExec=" + desktopString(filepath.Clean(spec.Executable)) + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	return []byte(content), nil
}

func desktopExecArg(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\x60", "\\\x60", "$", "\\$").Replace(value)
	return "\"" + value + "\""
}

func desktopString(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(value)
}

func desktopBoolean(data []byte, key string) bool {
	prefix := strings.ToLower(key) + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			value := strings.TrimSpace(line[len(prefix):])
			return strings.EqualFold(value, "true")
		}
	}
	return false
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func linuxUrgency(urgency Urgency) string {
	switch urgency {
	case UrgencyLow:
		return "low"
	case UrgencyCritical:
		return "critical"
	default:
		return "normal"
	}
}

var _ Backend = (*LinuxBackend)(nil)
