//go:build windows

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// dpiAwarenessPrelude opts the spawned powershell.exe into per-monitor-v2 DPI
// awareness before any WinForms window is created. filees-gui.exe itself is
// per-monitor-v2 aware via filees-gui.exe.manifest, but these dialogs are
// drawn by a separate powershell.exe process that does not inherit that
// manifest and defaults to DPI-unaware; on any scaled display Windows then
// bitmap-stretches the whole window, producing blurry text. Wrapped in
// try/catch because SetProcessDpiAwarenessContext requires Windows 10
// 1703+; older Windows 10 builds silently keep the unaware (but still
// functional) default.
const dpiAwarenessPrelude = "try{Add-Type -Name Dpi -Namespace Native -MemberDefinition '[DllImport(\"user32.dll\")] public static extern bool SetProcessDpiAwarenessContext(IntPtr value); [DllImport(\"user32.dll\")] public static extern uint GetDpiForSystem();';[Native.Dpi]::SetProcessDpiAwarenessContext([IntPtr](-4))|Out-Null}catch{};"

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
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsoleWindow(cmd)
	return cmd.Output()
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
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;$d=New-Object System.Windows.Forms.FolderBrowserDialog;"
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
	sb.WriteString(dpiAwarenessPrelude)
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
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;[System.Windows.Forms.MessageBox]::Show(" + psString(request.Text) + "," + psString(request.Title) + ",'OK','Information')|Out-Null"
	if err := b.runner.Run(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script); err != nil {
		return NewOperationalFailure("info_dialog", err)
	}
	return nil
}

// ShowSettings uses the native information window until the interactive
// WinForms settings table is introduced. The same complete overview remains
// available on Windows without exposing lifecycle actions through the tray.
func (b *WindowsBackend) ShowSettings(ctx context.Context, request SettingsDialogRequest) (SettingsDialogResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return SettingsDialogResult{}, NewUnavailable("settings_dialog", err)
	}
	script, err := buildSettingsDialogScript(request)
	if err != nil {
		return SettingsDialogResult{}, NewOperationalFailure("settings_dialog", err)
	}
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctx.Err() != nil {
			return SettingsDialogResult{}, ctx.Err()
		}
		return SettingsDialogResult{}, NewOperationalFailure("settings_dialog", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(output)), "|", 3)
	if len(parts) != 3 {
		return SettingsDialogResult{Action: SettingsDialogClose}, nil
	}
	result := SettingsDialogResult{ServerID: parts[1], RepoID: parts[2]}
	if strings.HasPrefix(parts[1], "@recovery-download:") && parts[0] == "download_recovery" {
		return SettingsDialogResult{Action: SettingsDialogDownloadRecovery, OperationID: strings.TrimPrefix(parts[1], "@recovery-download:")}, nil
	}
	switch parts[0] {
	case "add":
		result.Action = SettingsDialogAddFolder
	case "detach":
		result.Action = SettingsDialogDetachFolder
	case "delete":
		result.Action = SettingsDialogDeleteRepo
	case "load_dump":
		result.Action = SettingsDialogLoadDump
	case "manage_grants":
		result.Action = SettingsDialogManageGrants
	case "realm_visibility":
		result.Action = SettingsDialogRealmVisibility
	case "deactivate":
		result.Action = SettingsDialogDetachServer
	case "remove_realm":
		result.Action = SettingsDialogRemoveRealm
	default:
		result.Action = SettingsDialogClose
	}
	return result, nil
}

// settingsButtons is the single source of truth for the settings dialog's
// action bar. Each entry ties a button to the action it emits and to the
// hidden boolean column that decides whether it is enabled for the currently
// selected row.
//
// Every button is gated, including deactivate/remove_realm, so that a
// recovery row -- which has no server or folder behind it, only an operation
// ID -- cannot dispatch a folder or server action. linux.go gets that for
// free because it resolves recovery rows before it ever offers an action
// list; the Windows dialog shows one permanent button bar instead, so the
// same rule has to be expressed per row.
var settingsButtons = []struct {
	label, action, capability string
	left                      int
}{
	{"Widoczność", "realm_visibility", "CanVisibility", 75},
	{"Dostęp stref", "manage_grants", "CanGrants", 190},
	{"Dodaj folder", "add", "CanAdd", 305},
	{"Odłącz folder", "detach", "CanDetach", 420},
	{"Odłącz trwale", "delete", "CanDelete", 535},
	{"Odtwórz z archiwum", "load_dump", "CanLoadDump", 650},
	{"Dezaktywuj klienta", "deactivate", "CanDeactivate", 765},
	{"Usuń udział FileES", "remove_realm", "CanRemoveRealm", 880},
	{"Pobierz archiwa", "download_recovery", "CanDownloadRecovery", 995},
}

// settingsRow carries one grid row plus one boolean per action button. The
// Can* fields mirror the controller's own guards (actions.go) exactly, the
// same way linux.go's settingsAction gates its list items -- offering an
// action the controller will silently refuse is a real, reported "click it,
// nothing happens" bug, not a cosmetic issue.
type settingsRow struct {
	ServerID, RepoID, Server, Address, Realm, Folder, Path, State, Access string
	CanVisibility, CanGrants, CanAdd, CanDetach, CanDelete, CanLoadDump   bool
	CanDeactivate, CanRemoveRealm, CanDownloadRecovery                    bool
}

func buildSettingsDialogScript(request SettingsDialogRequest) (string, error) {
	rows := []settingsRow{}
	for _, s := range request.Servers {
		base := settingsRow{
			ServerID: s.ID, Server: s.Name, Address: s.Address, Realm: s.Realm,
			CanVisibility: s.CanSetRealmVisibility, CanAdd: s.CanAddFolder,
			CanDeactivate: true, CanRemoveRealm: true,
		}
		if len(s.Folders) == 0 {
			placeholder := base
			placeholder.Folder, placeholder.Path, placeholder.State, placeholder.Access = "Brak folderów", "—", "—", "—"
			rows = append(rows, placeholder)
			continue
		}
		for _, f := range s.Folders {
			folder := base
			folder.RepoID, folder.Folder, folder.Path, folder.State, folder.Access = f.ID, f.Name, f.LocalPath, f.State, f.Access
			folder.CanGrants, folder.CanDetach, folder.CanDelete, folder.CanLoadDump = f.CanManageGrants, f.CanDetach, f.CanDelete, f.CanLoadDump
			rows = append(rows, folder)
		}
	}
	for _, recovery := range request.Recoveries {
		id := "@recovery-grace:" + recovery.OperationID
		if recovery.CanDownload {
			id = "@recovery-download:" + recovery.OperationID
		}
		rows = append(rows, settingsRow{ServerID: id, Server: recovery.ServerName, Address: "—", Realm: "—", Folder: recovery.Status, Path: recovery.KitPath, State: "recovery", Access: "—", CanDownloadRecovery: recovery.CanDownload})
	}
	payload, err := json.Marshal(struct {
		Title, Text string
		Rows        []settingsRow
	}{request.Title, request.Text, rows})
	if err != nil {
		return "", err
	}

	// Column names and the values pushed into each row are generated from one
	// list so the two can never drift apart.
	hidden := []string{"ServerID", "RepoID"}
	for _, button := range settingsButtons {
		hidden = append(hidden, button.capability)
	}
	columns := append(append([]string{}, hidden...), "Serwer", "Adres", "Strefa", "Folder", "Ścieżka", "Stan", "Dostęp")
	quotedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		quotedColumns = append(quotedColumns, psString(column))
	}
	values := make([]string, 0, len(columns))
	for _, column := range columns {
		values = append(values, "$r."+settingsRowField(column))
	}
	quotedHidden := make([]string, 0, len(hidden))
	for _, column := range hidden {
		quotedHidden = append(quotedHidden, psString(column))
	}

	encoded := base64.StdEncoding.EncodeToString(payload)
	var sb strings.Builder
	sb.WriteString(dpiAwarenessPrelude)
	sb.WriteString("Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;")
	sb.WriteString("$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + psString(encoded) + "))|ConvertFrom-Json;$f=New-Object System.Windows.Forms.Form;$f.Text=$d.Title;$f.Width=1320;$f.Height=620;$f.StartPosition='CenterScreen';$l=New-Object System.Windows.Forms.Label;$l.Text=$d.Text;$l.Left=12;$l.Top=12;$l.Width=1270;$l.Height=34;$f.Controls.Add($l);")
	sb.WriteString("$g=New-Object System.Windows.Forms.DataGridView;$g.Left=12;$g.Top=52;$g.Width=1270;$g.Height=470;$g.ReadOnly=$true;$g.AllowUserToAddRows=$false;$g.SelectionMode='FullRowSelect';$g.MultiSelect=$false;$g.AutoSizeColumnsMode='Fill';$t=New-Object System.Data.DataTable;foreach($c in @(" + strings.Join(quotedColumns, ",") + ")){[void]$t.Columns.Add($c)};foreach($r in $d.Rows){[void]$t.Rows.Add(" + strings.Join(values, ",") + ")};$script:answer='close';")
	// The answer is "action|serverID|repoID". The separator is "|", not ":",
	// because a recovery row's ServerID is itself "@recovery-download:<id>" --
	// with ":" the reply split into four fields, the len()==3 guard rejected
	// it and the archive-download button silently did nothing. ShowRealmGrants
	// and ShowReservations already use "|" for the same reason; IDs never
	// contain it (linux.go joins on "|" too).
	sb.WriteString("function act($a){if($g.CurrentRow -ne $null){$script:answer=$a+'|'+[string]$g.CurrentRow.Cells['ServerID'].Value+'|'+[string]$g.CurrentRow.Cells['RepoID'].Value;$f.Close()}};")
	sb.WriteString("$btns=@{};")
	for _, button := range settingsButtons {
		sb.WriteString("$b=New-Object System.Windows.Forms.Button;$b.Text=" + psString(button.label) + ";$b.Width=108;$b.Height=28;$b.Left=" + fmt.Sprint(button.left) + ";$b.Top=540;$b.Add_Click({act " + psString(button.action) + "});$btns[" + psString(button.capability) + "]=$b;$f.Controls.Add($b);")
	}
	sb.WriteString("function updateButtons{foreach($k in @($btns.Keys)){$btns[$k].Enabled=($g.CurrentRow -ne $null -and [string]$g.CurrentRow.Cells[$k].Value -eq 'True')}};")
	// CurrentCellChanged, not SelectionChanged: DataGridView raises
	// SelectionChanged while CurrentRow still points at the row being left, so
	// gating off it lagged one row behind -- confirmed live, a row whose
	// capability was False kept the previous row's enabled button.
	// DataBindingComplete covers the initial state, because the columns do not
	// exist until binding completes.
	sb.WriteString("$g.Add_CurrentCellChanged({updateButtons});$g.Add_DataBindingComplete({foreach($n in @(" + strings.Join(quotedHidden, ",") + ")){if($g.Columns[$n] -ne $null){$g.Columns[$n].Visible=$false}};updateButtons});$g.DataSource=$t;$f.Controls.Add($g);updateButtons;")
	sb.WriteString("$c=New-Object System.Windows.Forms.Button;$c.Text='Zamknij';$c.Width=100;$c.Height=28;$c.Left=1130;$c.Top=540;$c.DialogResult='Cancel';$f.CancelButton=$c;$f.Controls.Add($c);[void]$f.ShowDialog();$script:answer")
	return sb.String(), nil
}

// settingsRowField maps a grid column name to the settingsRow JSON field that
// feeds it. Only the display columns differ from their field name.
func settingsRowField(column string) string {
	switch column {
	case "Serwer":
		return "Server"
	case "Adres":
		return "Address"
	case "Strefa":
		return "Realm"
	case "Folder":
		return "Folder"
	case "Ścieżka":
		return "Path"
	case "Stan":
		return "State"
	case "Dostęp":
		return "Access"
	default:
		return column
	}
}

func (b *WindowsBackend) ShowRealmGrants(ctx context.Context, request RealmGrantDialogRequest) (RealmGrantDialogResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return RealmGrantDialogResult{}, NewUnavailable("realm_grant_dialog", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return RealmGrantDialogResult{}, NewOperationalFailure("realm_grant_dialog", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	script := dpiAwarenessPrelude +
		"Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;" +
		"$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + psString(encoded) + "))|ConvertFrom-Json;" +
		"$f=New-Object System.Windows.Forms.Form;$f.Text=$d.Title;$f.Width=760;$f.Height=520;$f.StartPosition='CenterScreen';" +
		"$l=New-Object System.Windows.Forms.Label;$l.Text=$d.Text;$l.Left=12;$l.Top=12;$l.Width=710;$l.Height=44;$f.Controls.Add($l);" +
		"$g=New-Object System.Windows.Forms.DataGridView;$g.Left=12;$g.Top=62;$g.Width=710;$g.Height=360;$g.ReadOnly=$true;$g.AllowUserToAddRows=$false;$g.SelectionMode='FullRowSelect';$g.MultiSelect=$false;$g.AutoSizeColumnsMode='Fill';" +
		"$t=New-Object System.Data.DataTable;[void]$t.Columns.Add('RealmID');[void]$t.Columns.Add('Strefa');foreach($r in $d.Recipients){$name=$r.Alias;if([string]::IsNullOrWhiteSpace($name)){$name=$r.RealmID};[void]$t.Rows.Add($r.RealmID,$name)};$g.DataSource=$t;$g.Columns['RealmID'].Visible=$false;$f.Controls.Add($g);$script:answer='close';" +
		"function grant($a){if($g.CurrentRow -ne $null){$script:answer=$a+'|'+[string]$g.CurrentRow.Cells['RealmID'].Value;$f.Close()}};" +
		"$r=New-Object System.Windows.Forms.Button;$r.Text='Tylko odczyt';$r.Left=180;$r.Top=438;$r.Width=115;$r.Add_Click({grant 'grant_read'});$f.Controls.Add($r);" +
		"$w=New-Object System.Windows.Forms.Button;$w.Text='Odczyt i zapis';$w.Left=305;$w.Top=438;$w.Width=115;$w.Add_Click({grant 'grant_write'});$f.Controls.Add($w);" +
		"$x=New-Object System.Windows.Forms.Button;$x.Text='Cofnij dostęp';$x.Left=430;$x.Top=438;$x.Width=115;$x.Add_Click({grant 'revoke'});$f.Controls.Add($x);" +
		"$c=New-Object System.Windows.Forms.Button;$c.Text='Anuluj';$c.Left=580;$c.Top=438;$c.Width=100;$c.DialogResult='Cancel';$f.CancelButton=$c;$f.Controls.Add($c);[void]$f.ShowDialog();$script:answer"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctx.Err() != nil {
			return RealmGrantDialogResult{}, ctx.Err()
		}
		return RealmGrantDialogResult{}, NewOperationalFailure("realm_grant_dialog", err)
	}
	action, realmID, ok := strings.Cut(strings.TrimSpace(string(output)), "|")
	if !ok || realmID == "" {
		return RealmGrantDialogResult{Action: RealmGrantDialogClose}, nil
	}
	result := RealmGrantDialogResult{RealmID: realmID}
	switch action {
	case "grant_read":
		result.Action = RealmGrantDialogRead
	case "grant_write":
		result.Action = RealmGrantDialogWrite
	case "revoke":
		result.Action = RealmGrantDialogRevoke
	default:
		result.Action = RealmGrantDialogClose
	}
	return result, nil
}

func (b *WindowsBackend) ShowRealmVisibility(ctx context.Context, request RealmVisibilityDialogRequest) (RealmVisibilityDialogResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return RealmVisibilityDialogResult{}, NewUnavailable("realm_visibility_dialog", err)
	}
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;$r=[System.Windows.Forms.MessageBox]::Show(" + psString(request.Text) + "," + psString(request.Title) + ",'YesNoCancel','Question');if($r-eq'Yes'){'listed'}elseif($r-eq'No'){'hidden'}else{'close'}"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctx.Err() != nil {
			return RealmVisibilityDialogResult{}, ctx.Err()
		}
		return RealmVisibilityDialogResult{}, NewOperationalFailure("realm_visibility_dialog", err)
	}
	switch strings.TrimSpace(string(output)) {
	case "listed":
		return RealmVisibilityDialogResult{Action: RealmVisibilityDialogListed}, nil
	case "hidden":
		return RealmVisibilityDialogResult{Action: RealmVisibilityDialogPrivate}, nil
	default:
		return RealmVisibilityDialogResult{Action: RealmVisibilityDialogClose}, nil
	}
}

func (b *WindowsBackend) Confirm(ctx context.Context, request ConfirmRequest) (bool, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return false, NewUnavailable("confirm_dialog", err)
	}
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;$r=[System.Windows.Forms.MessageBox]::Show(" + psString(request.Text) + "," + psString(request.Title) + ",'YesNo','Question');if($r-eq'Yes'){'yes'}else{'no'}"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, NewOperationalFailure("confirm_dialog", err)
	}
	return strings.TrimSpace(string(output)) == "yes", nil
}

func (b *WindowsBackend) ConfirmConsent(ctx context.Context, request ConsentRequest) (ConsentResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return ConsentResult{}, NewUnavailable("consent_dialog", err)
	}
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;" +
		"$f=New-Object Windows.Forms.Form;$f.Text=" + psString(request.Title) + ";$f.Width=720;$f.Height=310;$f.StartPosition='CenterScreen';" +
		"$l=New-Object Windows.Forms.Label;$l.Text=" + psString(request.Text) + ";$l.Left=15;$l.Top=15;$l.Width=670;$l.Height=75;$f.Controls.Add($l);" +
		"$r=New-Object Windows.Forms.CheckBox;$r.Text=" + psString(request.RequiredText) + ";$r.Left=15;$r.Top=100;$r.Width=670;$r.Height=45;$f.Controls.Add($r);" +
		"$o=New-Object Windows.Forms.CheckBox;$o.Text=" + psString(request.OptionalText) + ";$o.Left=15;$o.Top=155;$o.Width=670;$o.Height=45;$f.Controls.Add($o);" +
		"$ok=New-Object Windows.Forms.Button;$ok.Text='Kontynuuj';$ok.Left=480;$ok.Top=220;$ok.Width=100;$ok.DialogResult='OK';$f.AcceptButton=$ok;$f.Controls.Add($ok);" +
		"$c=New-Object Windows.Forms.Button;$c.Text='Anuluj';$c.Left=590;$c.Top=220;$c.Width=90;$c.DialogResult='Cancel';$f.CancelButton=$c;$f.Controls.Add($c);" +
		"$d=$f.ShowDialog();if($d-eq'OK'){'ok|'+$r.Checked+'|'+$o.Checked}else{'cancel|false|false'}"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctx.Err() != nil {
			return ConsentResult{}, ctx.Err()
		}
		return ConsentResult{}, NewOperationalFailure("consent_dialog", err)
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "|")
	if len(parts) != 3 || parts[0] != "ok" {
		return ConsentResult{Cancelled: true}, nil
	}
	return ConsentResult{Required: strings.EqualFold(parts[1], "true"), Optional: strings.EqualFold(parts[2], "true")}, nil
}

// ShowReservations presents a real WinForms table rather than serialising a
// lock list into a browser or notification.  Only opaque GUI row IDs cross the
// PowerShell boundary; SVN lock tokens stay in the FileES process.
func (b *WindowsBackend) ShowReservations(ctx context.Context, request ReservationDialogRequest) (ReservationDialogResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return ReservationDialogResult{}, NewUnavailable("reservation_dialog", err)
	}
	script, err := buildReservationDialogScript(request)
	if err != nil {
		return ReservationDialogResult{}, NewOperationalFailure("reservation_dialog", err)
	}
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ReservationDialogResult{}, ctxErr
		}
		return ReservationDialogResult{}, NewOperationalFailure("reservation_dialog", err)
	}
	answer := strings.TrimSpace(string(output))
	if answer == "refresh" {
		return ReservationDialogResult{Action: ReservationDialogRefresh}, nil
	}
	if answer == "release_all" {
		return ReservationDialogResult{Action: ReservationDialogReleaseAll}, nil
	}
	if strings.HasPrefix(answer, "release:") {
		rowID := strings.TrimPrefix(answer, "release:")
		for _, row := range request.Rows {
			if row.ID == rowID {
				return ReservationDialogResult{Action: ReservationDialogRelease, RowID: rowID}, nil
			}
		}
	}
	return ReservationDialogResult{Action: ReservationDialogClose}, nil
}

func buildReservationDialogScript(request ReservationDialogRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	var sb strings.Builder
	sb.WriteString(dpiAwarenessPrelude)
	sb.WriteString("Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;")
	sb.WriteString("$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + psString(encoded) + "))|ConvertFrom-Json;")
	sb.WriteString("try{$s=[Native.Dpi]::GetDpiForSystem()/96.0}catch{$s=1.0};")
	sb.WriteString("$f=New-Object System.Windows.Forms.Form;$f.Text=$d.Title;$f.Width=[int](1160*$s);$f.Height=[int](620*$s);$f.StartPosition='CenterScreen';")
	sb.WriteString("$l=New-Object System.Windows.Forms.Label;$l.AutoSize=$false;$l.Left=[int](12*$s);$l.Top=[int](12*$s);$l.Width=[int](1110*$s);$l.Height=[int](38*$s);$l.Text=$d.Text;$f.Controls.Add($l);")
	sb.WriteString("$g=New-Object System.Windows.Forms.DataGridView;$g.Left=[int](12*$s);$g.Top=[int](58*$s);$g.Width=[int](1110*$s);$g.Height=[int](470*$s);$g.ReadOnly=$true;$g.AllowUserToAddRows=$false;$g.AllowUserToDeleteRows=$false;$g.SelectionMode='FullRowSelect';$g.MultiSelect=$false;$g.AutoSizeColumnsMode='Fill';")
	sb.WriteString("$t=New-Object System.Data.DataTable;[void]$t.Columns.Add('ID');[void]$t.Columns.Add('Serwer');[void]$t.Columns.Add('Kopia robocza');[void]$t.Columns.Add('Plik');[void]$t.Columns.Add('Właściciel');[void]$t.Columns.Add('Utworzono');[void]$t.Columns.Add('Działanie');foreach($r in $d.Rows){[void]$t.Rows.Add($r.ID,$r.Server,$r.WorkingCopy,$r.Path,$r.Owner,$r.CreatedAt,$r.Action)};$g.DataSource=$t;$g.Columns['ID'].Visible=$false;$f.Controls.Add($g);")
	sb.WriteString("$script:answer='close';$all=New-Object System.Windows.Forms.Button;$all.Text='Zwolnij wszystko';$all.Width=[int](150*$s);$all.Height=[int](28*$s);$all.Left=[int](630*$s);$all.Top=[int](540*$s);$all.Add_Click({$script:answer='release_all';$f.Close()});$f.Controls.Add($all);$release=New-Object System.Windows.Forms.Button;$release.Text='Zwolnij';$release.Width=[int](100*$s);$release.Height=[int](28*$s);$release.Left=[int](790*$s);$release.Top=[int](540*$s);$release.Add_Click({if($g.CurrentRow -ne $null){$script:answer='release:'+[string]$g.CurrentRow.Cells['ID'].Value;$f.Close()}});$f.Controls.Add($release);")
	sb.WriteString("$refresh=New-Object System.Windows.Forms.Button;$refresh.Text='Odśwież';$refresh.Width=[int](100*$s);$refresh.Height=[int](28*$s);$refresh.Left=[int](900*$s);$refresh.Top=[int](540*$s);$refresh.Add_Click({$script:answer='refresh';$f.Close()});$f.Controls.Add($refresh);")
	sb.WriteString("$close=New-Object System.Windows.Forms.Button;$close.Text='Zamknij';$close.Width=[int](100*$s);$close.Height=[int](28*$s);$close.Left=[int](1010*$s);$close.Top=[int](540*$s);$close.DialogResult='Cancel';$f.CancelButton=$close;$f.Controls.Add($close);[void]$f.ShowDialog();$script:answer")
	return sb.String(), nil
}

func buildPromptScript(request PromptTextRequest) string {
	var sb strings.Builder
	sb.WriteString(dpiAwarenessPrelude)
	sb.WriteString("Add-Type -AssemblyName System.Windows.Forms;")
	// WinForms' own AutoScaleMode/AutoScaleDimensions rescaling (tried and
	// confirmed ineffective here) needs the "high DPI improvements" opt-in
	// that .NET Framework WinForms only honors from the *host executable's*
	// app.config — powershell.exe's own config doesn't carry it and this
	// script cannot supply one, so AutoScaleMode is silently a no-op in this
	// process. Querying the real system DPI and scaling every literal pixel
	// value ourselves, before laying out any control, works regardless of
	// host configuration. Without it, the process being DPI-aware (see
	// dpiAwarenessPrelude) fixes the blur but leaves the literal 96-DPI pixel
	// sizes unscaled, which is what clipped the OK button and truncated the
	// title on a 250% display.
	sb.WriteString("try{$s=[Native.Dpi]::GetDpiForSystem()/96.0}catch{$s=1.0};")
	sb.WriteString("$f=New-Object System.Windows.Forms.Form;$f.Width=[int](520*$s);$f.Height=[int](190*$s);$f.StartPosition='CenterScreen';")
	sb.WriteString("$f.Text=" + psString(request.Title) + ";")
	// AutoSize=false with an explicit, scaled Height is required here: Label's
	// default AutoSize sizing is computed from the design-time (96 DPI) box
	// and does not grow to match the DPI-scaled font, so at 250%+ scaling the
	// larger glyphs get clipped against the control's own bounding box.
	sb.WriteString("$l=New-Object System.Windows.Forms.Label;$l.AutoSize=$false;$l.Left=[int](12*$s);$l.Top=[int](15*$s);$l.Width=[int](480*$s);$l.Height=[int](24*$s);$l.Text=" + psString(request.Text) + ";$f.Controls.Add($l);")
	sb.WriteString("$t=New-Object System.Windows.Forms.TextBox;$t.Left=[int](12*$s);$t.Top=[int](45*$s);$t.Width=[int](480*$s);")
	if request.Placeholder != "" {
		sb.WriteString("$t.Text=" + psString(request.Placeholder) + ";")
	}
	if request.Secret {
		sb.WriteString("$t.UseSystemPasswordChar=$true;")
	}
	sb.WriteString("$f.Controls.Add($t);$b=New-Object System.Windows.Forms.Button;$b.Text='OK';$b.Width=[int](75*$s);$b.Height=[int](23*$s);$b.Left=[int](412*$s);$b.Top=[int](82*$s);$b.DialogResult='OK';$f.AcceptButton=$b;$f.Controls.Add($b);")
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
