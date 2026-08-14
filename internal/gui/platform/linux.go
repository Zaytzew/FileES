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
	if !request.AllowOutsideRoot {
		if err := requireAbsolutePath(request.Root); err != nil {
			return PickFilesResult{}, NewOperationalFailure("file_picker", fmt.Errorf("root: %w", err))
		}
	}
	initialDir := request.InitialDir
	if initialDir == "" {
		initialDir = request.Root
	}
	if request.AllowOutsideRoot {
		if err := requireAbsolutePath(initialDir); err != nil {
			return PickFilesResult{}, NewOperationalFailure("file_picker", fmt.Errorf("initial directory: %w", err))
		}
	} else if err := requirePathInsideRoot(initialDir, request.Root); err != nil {
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
		if request.AllowOutsideRoot {
			if err := requireAbsolutePath(path); err != nil {
				return PickFilesResult{}, NewOperationalFailure("file_picker", fmt.Errorf("selected path: %w", err))
			}
		} else if err := requirePathInsideRoot(path, request.Root); err != nil {
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
	text := request.Text
	if request.Placeholder != "" {
		// yad has no placeholder concept and --entry-text is a real value, so
		// the hint goes into the prompt instead of into the field. Putting it
		// in the field would submit it, which is exactly the bug this split
		// exists to remove.
		if text != "" {
			text += "\n"
		}
		text += "(" + request.Placeholder + ")"
	}
	if text != "" {
		args = append(args, "--text="+text)
	}
	if request.Default != "" {
		args = append(args, "--entry-text="+request.Default)
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

// ShowProgress opens a pulsating zenity window and returns its closer. See the
// ProgressPresenter contract: the window ends when the controller says so, not
// when the user acts.
//
// --pulsate is used deliberately without ever writing a percentage to the
// child: the daemon does not measure import progress, so the bar reports
// activity, not completion. --no-cancel removes the only button that could
// desynchronise the window from the operation behind it.
//
// The child runs under a derived context; cancelling it kills zenity, which
// closes the window. close() waits for the process to be reaped.
func (b *LinuxBackend) ShowProgress(ctx context.Context, request ProgressRequest) (func(), error) {
	command, err := b.runner.LookPath("zenity")
	if err != nil {
		return nil, NewUnavailable("progress_dialog", errors.New("zenity is not installed"))
	}
	args := []string{
		"--progress", "--pulsate", "--no-cancel", "--width=460",
		"--title=" + request.Title, "--text=" + request.Text,
	}
	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Killing the child is the normal exit path, so its error is expected
		// and must not reach the user: this window is a hint, not an operation.
		_, _ = b.runner.Output(child, command, args...)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, nil
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

// ShowReservations uses Zenity's native list window. The action column names
// the operation offered for a row rather than exposing implementation states;
// the action layer remains the final authority for whether a selected row can
// be freed. The returned ID is an opaque GUI-local handle, never an SVN lock
// token.
func (b *LinuxBackend) ShowReservations(ctx context.Context, request ReservationDialogRequest) (ReservationDialogResult, error) {
	command, err := b.runner.LookPath("zenity")
	if err != nil {
		return ReservationDialogResult{}, NewUnavailable("reservation_dialog", errors.New("zenity is not installed"))
	}
	args := []string{
		"--list", "--radiolist", "--title=" + request.Title,
		"--text=" + request.Text, "--width=1200", "--height=560",
		"--column=", "--column=ID", "--column=Serwer", "--column=Kopia robocza", "--column=Plik",
		"--column=Właściciel", "--column=Utworzono", "--column=Działanie",
		"--hide-column=2", "--print-column=2", "--ok-label=Zwolnij",
		"--cancel-label=Zamknij", "--extra-button=Zwolnij wszystko", "--extra-button=Odśwież",
	}
	for _, row := range request.Rows {
		args = append(args, "FALSE", row.ID, row.Server, row.WorkingCopy, row.Path, row.Owner, row.CreatedAt, row.Action)
	}
	output, err := b.runner.Output(ctx, command, args...)
	// Zenity 4 returns the text of an extra button on stdout but exits with
	// status 1, the same status it uses for Cancel. Read a recognised explicit
	// button result before treating the exit code as cancellation.
	selection := strings.TrimSpace(string(output))
	if selection == "Odśwież" {
		return ReservationDialogResult{Action: ReservationDialogRefresh}, nil
	}
	if selection == "Zwolnij wszystko" {
		return ReservationDialogResult{Action: ReservationDialogReleaseAll}, nil
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ReservationDialogResult{}, ctxErr
		}
		if commandCancelled(err) {
			return ReservationDialogResult{Action: ReservationDialogClose}, nil
		}
		return ReservationDialogResult{}, NewOperationalFailure("reservation_dialog", err)
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

// applyLinuxDarkThemePreference nudges yad to match the desktop's dark-mode
// preference. Unlike zenity, yad does not reliably pick this up from the
// running session on its own. This only ever forces the dark variant, never
// the light one: Adwaita's default is already light, so a failed or
// negative detection (missing gsettings, non-GNOME desktop, explicit
// "default"/"prefer-light") simply leaves GTK_THEME untouched rather than
// overriding whatever the user or desktop already configured.
func (b *LinuxBackend) applyLinuxDarkThemePreference(ctx context.Context) {
	path, err := b.runner.LookPath("gsettings")
	if err != nil {
		return
	}
	output, err := b.runner.Output(ctx, path, "get", "org.gnome.desktop.interface", "color-scheme")
	if err != nil {
		return
	}
	if strings.Contains(string(output), "prefer-dark") {
		os.Setenv("GTK_THEME", "Adwaita:dark")
	}
}

// ShowSettings presents the server/folder overview as a native yad table.
// It is deliberately read-only at this stage; later buttons return only an
// opaque intent to the GUI controller.
func (b *LinuxBackend) ShowSettings(ctx context.Context, request SettingsDialogRequest) (SettingsDialogResult, error) {
	command, err := b.runner.LookPath("yad")
	if err != nil {
		return SettingsDialogResult{}, NewUnavailable("settings_dialog", errors.New("yad is not installed"))
	}
	b.applyLinuxDarkThemePreference(ctx)
	args := []string{
		"--list", "--radiolist", "--title=" + request.Title, "--text=" + SettingsText(SettingsDialogRequest{Text: request.Text}), "--width=1240", "--height=600",
		"--column=", "--column=ID", "--column=Serwer", "--column=Adres", "--column=Strefa", "--column=Repozytorium", "--column=Ścieżka lokalna", "--column=Stan", "--column=Dostęp", "--column=Edycja",
		"--hide-column=2", "--print-column=2", "--ok-label=Wybierz", "--cancel-label=Zamknij",
	}
	for _, server := range request.Servers {
		if len(server.Folders) == 0 {
			args = append(args, "FALSE", server.ID+"|", server.Name, server.Address, server.Realm, "Brak folderów", "—", "—", "—", "—")
			continue
		}
		for _, folder := range server.Folders {
			args = append(args, "FALSE", server.ID+"|"+folder.ID, server.Name, server.Address, server.Realm, folder.Name, folder.LocalPath, folder.State, folder.Access, folder.Editing)
		}
	}
	for _, recovery := range request.Recoveries {
		prefix := "@recovery-grace:"
		if recovery.CanDownload {
			prefix = "@recovery-download:"
		}
		args = append(args, "FALSE", prefix+recovery.OperationID+"|", recovery.ServerName, "—", "—", recovery.Status, recovery.KitPath, "recovery", "—", "—")
	}
	output, err := b.runner.Output(ctx, command, args...)
	selection := yadSelection(output)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SettingsDialogResult{}, ctxErr
		}
		if commandCancelled(err) {
			return SettingsDialogResult{Action: SettingsDialogClose}, nil
		}
		return SettingsDialogResult{}, NewOperationalFailure("settings_dialog", err)
	}
	serverID, repoID, ok := strings.Cut(selection, "|")
	if !ok || serverID == "" {
		return SettingsDialogResult{Action: SettingsDialogClose}, nil
	}
	if strings.HasPrefix(serverID, "@recovery-download:") {
		return SettingsDialogResult{Action: SettingsDialogDownloadRecovery, OperationID: strings.TrimPrefix(serverID, "@recovery-download:")}, nil
	}
	if strings.HasPrefix(serverID, "@recovery-grace:") {
		return SettingsDialogResult{Action: SettingsDialogClose}, nil
	}
	canAddFolder := false
	canConnect := false
	canLocate := false
	canManageGrants := false
	canSetEditingPolicy := false
	canManagePublicShares := false
	canDetach := false
	canDelete := false
	canLoadDump := false
	canSetRealmVisibility := false
	canSetRealmBranding := false
	for _, server := range request.Servers {
		if server.ID != serverID {
			continue
		}
		canAddFolder = server.CanAddFolder
		canSetRealmVisibility = server.CanSetRealmVisibility
		canSetRealmBranding = server.CanSetRealmBranding
		for _, folder := range server.Folders {
			if folder.ID == repoID {
				canConnect = folder.CanConnect
				canLocate = folder.CanLocate
				canManageGrants = folder.CanManageGrants
				canSetEditingPolicy = folder.CanSetEditingPolicy
				canManagePublicShares = folder.CanManagePublicShares
				canDetach = folder.CanDetach
				canDelete = folder.CanDelete
				canLoadDump = folder.CanLoadDump
				break
			}
		}
	}
	action, err := b.settingsAction(ctx, command, repoID != "", canAddFolder, canConnect, canLocate, canManageGrants, canSetEditingPolicy, canManagePublicShares, canDetach, canDelete, canLoadDump, canSetRealmVisibility, canSetRealmBranding)
	if err != nil || action == SettingsDialogClose {
		return SettingsDialogResult{Action: action}, err
	}
	result := SettingsDialogResult{Action: action, ServerID: serverID, RepoID: repoID}
	if action == SettingsDialogConnectRepos {
		result.RepoIDs = []string{repoID}
	}
	return result, nil
}

// ShowJournal uses the same combined chronology as Windows. Yad does not
// expose reliable per-row font styling, so the presentation model's explicit
// warning prefix remains the error emphasis on Linux.
func (b *LinuxBackend) ShowJournal(ctx context.Context, request JournalDialogRequest) error {
	command, err := b.runner.LookPath("yad")
	if err != nil {
		return NewUnavailable("journal_dialog", errors.New("yad is not installed"))
	}
	b.applyLinuxDarkThemePreference(ctx)
	args := []string{
		"--list", "--title=" + request.Title, "--text=" + request.Text,
		"--width=1240", "--height=680", "--button=Zamknij:0",
		"--column=Czas", "--column=Repozytorium", "--column=Wpis", "--column=Szczegóły",
	}
	for _, row := range request.Rows {
		args = append(args, row.Timestamp, row.Repository, row.Summary, row.Details)
	}
	if _, err := b.runner.Output(ctx, command, args...); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if commandCancelled(err) {
			return nil
		}
		return NewOperationalFailure("journal_dialog", err)
	}
	return nil
}

func (b *LinuxBackend) settingsAction(ctx context.Context, command string, hasFolder, canAddFolder, canConnect, canLocate, canManageGrants, canSetEditingPolicy, canManagePublicShares, canDetach, canDelete, canLoadDump, canSetRealmVisibility, canSetRealmBranding bool) (SettingsDialogAction, error) {
	args := []string{"--list", "--radiolist", "--title=Ustawienia FileES", "--text=Wybierz działanie:", "--column=", "--column=ID", "--column=Działanie", "--hide-column=2", "--print-column=2", "--ok-label=Wykonaj", "--cancel-label=Anuluj", "FALSE", "detach_server", "Dezaktywuj tylko tego klienta", "FALSE", "remove_realm", "Usuń mój udział FileES z serwera"}
	if canAddFolder {
		args = append(args, "FALSE", "add_folder", "Dodaj folder do FileES")
	}
	if canConnect {
		args = append(args, "FALSE", "connect_repositories", "Połącz z lokalnym folderem")
	}
	if canLocate {
		args = append(args, "FALSE", "locate_folder", "Wskaż przeniesioną kopię roboczą")
	}
	if canSetRealmVisibility {
		args = append(args, "FALSE", "realm_visibility", "Widoczność mojej strefy")
	}
	if canSetRealmBranding {
		args = append(args, "FALSE", "realm_branding", "Wygląd udziałów publicznych")
	}
	if hasFolder {
		if canManageGrants {
			args = append(args, "FALSE", "manage_grants", "Uprawnienia gości")
		}
		if canSetEditingPolicy {
			args = append(args, "FALSE", "editing_policy", "Zasady edycji")
		}
		if canManagePublicShares {
			args = append(args, "FALSE", "public_shares", "Udostępnienia publiczne")
		}
		if canDetach {
			args = append(args, "FALSE", "detach_folder", "Odłącz tylko folder")
		}
		if canDelete {
			args = append(args, "FALSE", "delete_repository", "Odłącz trwale repozytorium")
		}
		if canLoadDump {
			args = append(args, "FALSE", "load_dump", "Odtwórz z archiwum")
		}
	}
	output, err := b.runner.Output(ctx, command, args...)
	if err != nil {
		if ctx.Err() != nil {
			return SettingsDialogClose, ctx.Err()
		}
		if commandCancelled(err) {
			return SettingsDialogClose, nil
		}
		return SettingsDialogClose, NewOperationalFailure("settings_dialog", err)
	}
	return settingsAction(yadSelection(output)), nil
}

func (b *LinuxBackend) ShowRealmVisibility(ctx context.Context, request RealmVisibilityDialogRequest) (RealmVisibilityDialogResult, error) {
	command, err := b.runner.LookPath("yad")
	if err != nil {
		return RealmVisibilityDialogResult{}, NewUnavailable("realm_visibility_dialog", errors.New("yad is not installed"))
	}
	b.applyLinuxDarkThemePreference(ctx)
	output, err := b.runner.Output(ctx, command, "--list", "--radiolist", "--title="+request.Title, "--text="+request.Text, "--column=", "--column=ID", "--column=Widoczność", "--hide-column=2", "--print-column=2", "--ok-label=Wybierz", "--cancel-label=Anuluj", "FALSE", "listed", "Widoczny w prywatnym katalogu odbiorców", "FALSE", "hidden", "Ukryty")
	if err != nil {
		if ctx.Err() != nil {
			return RealmVisibilityDialogResult{}, ctx.Err()
		}
		if commandCancelled(err) {
			return RealmVisibilityDialogResult{Action: RealmVisibilityDialogClose}, nil
		}
		return RealmVisibilityDialogResult{}, NewOperationalFailure("realm_visibility_dialog", err)
	}
	switch yadSelection(output) {
	case "listed":
		return RealmVisibilityDialogResult{Action: RealmVisibilityDialogListed}, nil
	case "hidden":
		return RealmVisibilityDialogResult{Action: RealmVisibilityDialogPrivate}, nil
	default:
		return RealmVisibilityDialogResult{Action: RealmVisibilityDialogClose}, nil
	}
}

func (b *LinuxBackend) ShowRealmGrants(ctx context.Context, request RealmGrantDialogRequest) (RealmGrantDialogResult, error) {
	command, err := b.runner.LookPath("yad")
	if err != nil {
		return RealmGrantDialogResult{}, NewUnavailable("realm_grant_dialog", errors.New("yad is not installed"))
	}
	b.applyLinuxDarkThemePreference(ctx)
	args := []string{"--list", "--radiolist", "--title=" + request.Title, "--text=" + request.Text, "--width=820", "--height=520", "--column=", "--column=ID", "--column=Gość", "--column=Aktualne", "--column=Ustaw", "--hide-column=2", "--print-column=2", "--ok-label=Zastosuj", "--cancel-label=Anuluj"}
	for _, recipient := range request.Recipients {
		label := strings.TrimSpace(recipient.Alias)
		if label == "" {
			label = recipient.RealmID
		}
		current := "brak"
		if recipient.State == "active" && recipient.Access == "r" {
			current = "tylko odczyt"
		} else if recipient.State == "active" && recipient.Access == "rw" {
			current = "odczyt i zapis"
		}
		args = append(args,
			"FALSE", recipient.RealmID+"|r", label, current, "Tylko odczyt",
			"FALSE", recipient.RealmID+"|rw", label, current, "Odczyt i zapis",
			"FALSE", recipient.RealmID+"|revoke", label, current, "Cofnij dostęp",
		)
	}
	output, err := b.runner.Output(ctx, command, args...)
	if err != nil {
		if ctx.Err() != nil {
			return RealmGrantDialogResult{}, ctx.Err()
		}
		if commandCancelled(err) {
			return RealmGrantDialogResult{Action: RealmGrantDialogClose}, nil
		}
		return RealmGrantDialogResult{}, NewOperationalFailure("realm_grant_dialog", err)
	}
	realmID, access, ok := strings.Cut(yadSelection(output), "|")
	if !ok || realmID == "" {
		return RealmGrantDialogResult{Action: RealmGrantDialogClose}, nil
	}
	action := RealmGrantDialogClose
	switch access {
	case "r":
		action = RealmGrantDialogRead
	case "rw":
		action = RealmGrantDialogWrite
	case "revoke":
		action = RealmGrantDialogRevoke
	}
	return RealmGrantDialogResult{Action: action, RealmID: realmID}, nil
}

func (b *LinuxBackend) ShowPublicShares(ctx context.Context, request PublicShareDialogRequest) (PublicShareDialogResult, error) {
	command, err := b.runner.LookPath("yad")
	if err != nil {
		return PublicShareDialogResult{}, NewUnavailable("public_share_dialog", errors.New("yad is not installed"))
	}
	b.applyLinuxDarkThemePreference(ctx)
	args := []string{"--list", "--radiolist", "--title=" + request.Title, "--text=" + request.Text, "--width=1100", "--height=620", "--column=", "--column=ID", "--column=Adres", "--column=Stan", "--column=Folder źródłowy", "--column=Odbiorcy", "--column=Hasło", "--column=Rewizja", "--column=Działanie", "--hide-column=2", "--print-column=2", "--ok-label=Wykonaj", "--cancel-label=Zamknij", "FALSE", "create|", "—", "—", "—", "—", "—", "—", "Nowe udostępnienie"}
	for _, share := range request.Shares {
		for _, action := range []struct{ id, label string }{{"edit", "Edytuj"}, {"revoke", "Cofnij"}, {"delete", "Usuń"}} {
			args = append(args, "FALSE", action.id+"|"+share.ChannelID, share.Address, share.State, share.SourceRoot, share.Recipients, share.Password, share.Revision, action.label)
		}
	}
	output, err := b.runner.Output(ctx, command, args...)
	if err != nil {
		if ctx.Err() != nil {
			return PublicShareDialogResult{}, ctx.Err()
		}
		if commandCancelled(err) {
			return PublicShareDialogResult{Action: PublicShareDialogClose}, nil
		}
		return PublicShareDialogResult{}, NewOperationalFailure("public_share_dialog", err)
	}
	action, channelID, ok := strings.Cut(yadSelection(output), "|")
	if !ok {
		return PublicShareDialogResult{Action: PublicShareDialogClose}, nil
	}
	result := PublicShareDialogResult{ChannelID: channelID}
	switch action {
	case "create":
		result.Action = PublicShareDialogCreate
	case "edit":
		result.Action = PublicShareDialogEdit
	case "revoke":
		result.Action = PublicShareDialogRevoke
	case "delete":
		result.Action = PublicShareDialogDelete
	default:
		result.Action = PublicShareDialogClose
	}
	return result, nil
}

func (b *LinuxBackend) ConfirmConsent(ctx context.Context, request ConsentRequest) (ConsentResult, error) {
	command, err := b.runner.LookPath("zenity")
	if err != nil {
		return ConsentResult{}, NewUnavailable("consent_dialog", err)
	}
	output, err := b.runner.Output(ctx, command,
		"--list", "--checklist", "--title="+request.Title, "--text="+request.Text,
		"--column=", "--column=ID", "--column=Potwierdzenie", "--hide-column=2", "--print-column=2",
		"--separator=\n", "--ok-label=Kontynuuj", "--cancel-label=Anuluj",
		"TRUE", "required", request.RequiredText, "FALSE", "optional", request.OptionalText,
	)
	if err != nil {
		if ctx.Err() != nil {
			return ConsentResult{}, ctx.Err()
		}
		if commandCancelled(err) {
			return ConsentResult{Cancelled: true}, nil
		}
		return ConsentResult{}, NewOperationalFailure("consent_dialog", err)
	}
	selected := strings.Fields(string(output))
	result := ConsentResult{}
	for _, value := range selected {
		result.Required = result.Required || value == "required"
		result.Optional = result.Optional || value == "optional"
	}
	return result, nil
}

func settingsAction(label string) SettingsDialogAction {
	switch label {
	case "add_folder", "Dodaj folder":
		return SettingsDialogAddFolder
	case "connect_repositories", "Połącz":
		return SettingsDialogConnectRepos
	case "locate_folder":
		return SettingsDialogLocateFolder
	case "detach_folder", "Odłącz folder":
		return SettingsDialogDetachFolder
	case "delete_repository", "Odłącz trwale":
		return SettingsDialogDeleteRepo
	case "load_dump", "Odtwórz z archiwum":
		return SettingsDialogLoadDump
	case "manage_grants":
		return SettingsDialogManageGrants
	case "public_shares":
		return SettingsDialogPublicShares
	case "realm_visibility":
		return SettingsDialogRealmVisibility
	case "realm_branding":
		return SettingsDialogRealmBranding
	case "detach_server", "Dezaktywuj klienta":
		return SettingsDialogDetachServer
	case "remove_realm":
		return SettingsDialogRemoveRealm
	default:
		return SettingsDialogClose
	}
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
