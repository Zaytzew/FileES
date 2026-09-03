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

// foregroundPrelude is inserted immediately before every custom
// $f.ShowDialog() below.  Settings actions are a two-process hand-off:
// the foreground PowerShell window closes, control returns to the tray
// process, and the tray starts another PowerShell process for the follow-up
// dialog. Windows is allowed to reject Activate() for that second process.
// Turning TopMost off in Shown therefore recreated the reported "silent
// death": a fully functional modal window dropped behind the user's current
// application. Keep the short-lived modal window TopMost for its lifetime;
// it exits with the helper process and cannot pin the main application.
const foregroundPrelude = "$f.TopMost=$true;$f.Add_Shown({$f.Activate();$f.BringToFront()});"

// foregroundOwnerPrelude provides a live TopMost owner for stock common and
// message-box dialogs. The old helper hid the owner before ShowDialog, which
// removed the only z-order anchor and let the child open behind other apps.
// A one-pixel transparent owner remains alive for the modal lifetime without
// producing a taskbar button or a visible helper window.
const foregroundOwnerPrelude = "$owner=New-Object System.Windows.Forms.Form;$owner.StartPosition='CenterScreen';$owner.Width=1;$owner.Height=1;$owner.FormBorderStyle='None';$owner.ShowInTaskbar=$false;$owner.Opacity=0;$owner.TopMost=$true;$owner.Show();$owner.Activate();"

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

func (b *WindowsBackend) PickFiles(ctx context.Context, request PickFilesRequest) (PickFilesResult, error) {
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
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;$d=New-Object System.Windows.Forms.FolderBrowserDialog;$d.ShowNewFolderButton=$true;" + foregroundOwnerPrelude
	if request.Title != "" {
		script += "$d.Description=" + psString(request.Title) + ";"
	}
	if initialDir != "" {
		script += "$d.SelectedPath=" + psString(initialDir) + ";"
	}
	script += "if($d.ShowDialog($owner)-eq[System.Windows.Forms.DialogResult]::OK){$d.SelectedPath}else{exit 1}"
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
	sb.WriteString(foregroundOwnerPrelude)
	sb.WriteString("$d.InitialDirectory=" + psString(initialDir) + ";")
	if request.Title != "" {
		sb.WriteString("$d.Title=" + psString(request.Title) + ";")
	}
	if request.AllowMultiple {
		sb.WriteString("$d.Multiselect=$true;")
	}
	sb.WriteString("$r=$d.ShowDialog($owner);")
	sb.WriteString("if($r-eq[System.Windows.Forms.DialogResult]::OK){$d.FileNames -join \"`n\"}else{exit 1}")
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

// ShowProgress opens a modeless marquee window and returns its closer.
//
// Unlike every other dialog here the window is never dismissed by the user: it
// has no control box and no buttons, so the only way out is the returned close
// function killing the child. That is the point — it must stay on screen for
// exactly as long as the operation runs, and disappear the moment it ends.
//
// The child is spawned under a derived context; cancelling it terminates
// powershell.exe, which tears the form down with it. close() then waits for the
// process to be reaped so a caller that immediately opens another window does
// not race this one.
func (b *WindowsBackend) ShowProgress(ctx context.Context, request ProgressRequest) (func(), error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return nil, NewUnavailable("progress_dialog", err)
	}
	script := dpiAwarenessPrelude +
		"Add-Type -AssemblyName System.Windows.Forms;" +
		// A Marquee ProgressBar is drawn by the themed common control. Without
		// visual styles the classic control has no marquee at all and paints an
		// empty trough, which is exactly what a stalled operation would look
		// like. Measured on this box: RenderWithVisualStyles was False for a
		// plain -Command host, so the window came up with no bar.
		"[System.Windows.Forms.Application]::EnableVisualStyles();" +
		"$f=New-Object System.Windows.Forms.Form;" +
		"$f.Text=" + psString(request.Title) + ";" +
		"$f.FormBorderStyle='FixedDialog';$f.ControlBox=$false;$f.MaximizeBox=$false;$f.MinimizeBox=$false;" +
		"$f.StartPosition='CenterScreen';$f.Width=470;$f.Height=160;$f.TopMost=$true;$f.ShowInTaskbar=$false;" +
		"$l=New-Object System.Windows.Forms.Label;$l.Text=" + psString(request.Text) + ";" +
		"$l.Left=20;$l.Top=22;$l.Width=415;$l.Height=44;" +
		"$p=New-Object System.Windows.Forms.ProgressBar;$p.Style='Marquee';$p.MarqueeAnimationSpeed=30;" +
		"$p.Left=20;$p.Top=74;$p.Width=415;$p.Height=20;" +
		"$f.Controls.Add($l);$f.Controls.Add($p);" +
		"[System.Windows.Forms.Application]::Run($f)"

	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A killed child reports a non-nil error by design; the window is a
		// hint, so a failure to draw it must never surface as an operation
		// failure to the user.
		_ = b.runner.Run(child, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, nil
}

func (b *WindowsBackend) ShowInfo(ctx context.Context, request InfoRequest) error {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return NewUnavailable("info_dialog", err)
	}
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;" + foregroundOwnerPrelude + "[System.Windows.Forms.MessageBox]::Show($owner," + psString(request.Text) + "," + psString(request.Title) + ",'OK','Information')|Out-Null"
	if err := b.runner.Run(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script); err != nil {
		return NewOperationalFailure("info_dialog", err)
	}
	return nil
}

// ShowSettings runs the same server → action → folder sequence as Linux.
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
	case "connect":
		result.Action = SettingsDialogConnectRepos
		result.RepoID = ""
		for _, repoID := range strings.Split(parts[2], ",") {
			if repoID = strings.TrimSpace(repoID); repoID != "" {
				result.RepoIDs = append(result.RepoIDs, repoID)
			}
		}
	case "locate":
		result.Action = SettingsDialogLocateFolder
	case "detach":
		result.Action = SettingsDialogDetachFolder
	case "delete":
		result.Action = SettingsDialogDeleteRepo
	case "load_dump":
		result.Action = SettingsDialogLoadDump
	case "manage_grants":
		result.Action = SettingsDialogManageGrants
	case "editing_policy":
		result.Action = SettingsDialogEditingPolicy
	case "public_shares":
		result.Action = SettingsDialogPublicShares
	case "upload_channels":
		result.Action = SettingsDialogUploadChannels
	case "realm_visibility":
		result.Action = SettingsDialogRealmVisibility
	case "realm_branding":
		result.Action = SettingsDialogRealmBranding
	case "session_timeout":
		result.Action = SettingsDialogSessionTimeout
	case "deactivate":
		result.Action = SettingsDialogDetachServer
	case "remove_realm":
		result.Action = SettingsDialogRemoveRealm
	default:
		result.Action = SettingsDialogClose
	}
	return result, nil
}

func buildSettingsDialogScript(request SettingsDialogRequest) (string, error) {
	wizard := BuildSettingsWizard(request)
	payload, err := json.Marshal(wizard)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	var sb strings.Builder
	sb.WriteString(dpiAwarenessPrelude)
	sb.WriteString("Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;")
	sb.WriteString("$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + psString(encoded) + "))|ConvertFrom-Json;$script:answer='close';")
	// One reusable grid form. Sequence is server → action → folders, matching
	// linux.go. Folder-scoped options never require a dummy share pick first.
	sb.WriteString("function showGrid($title,$text,$width,$height,$cols,$add,$multi,$ok){$f=New-Object System.Windows.Forms.Form;$f.Text=$title;$f.Width=$width;$f.Height=$height;$f.StartPosition='CenterScreen';$l=New-Object System.Windows.Forms.Label;$l.Text=$text;$l.Left=12;$l.Top=12;$l.Width=($width-40);$l.Height=36;$f.Controls.Add($l);$g=New-Object System.Windows.Forms.DataGridView;$g.Left=12;$g.Top=52;$g.Width=($width-40);$g.Height=($height-140);$g.ReadOnly=$true;$g.AllowUserToAddRows=$false;$g.SelectionMode='FullRowSelect';$g.MultiSelect=$multi;$g.AutoSizeColumnsMode='Fill';$t=New-Object System.Data.DataTable;foreach($c in $cols){[void]$t.Columns.Add($c)};& $add $t;$g.DataSource=$t;for($i=0;$i-lt $g.Columns.Count;$i++){if($g.Columns[$i].Name-eq'ID'-or$g.Columns[$i].HeaderText-eq'ID'){$g.Columns[$i].Visible=$false}};$f.Controls.Add($g);$b=New-Object System.Windows.Forms.Button;$b.Text=$ok;$b.Width=120;$b.Height=28;$b.Left=($width-260);$b.Top=($height-70);$b.DialogResult='OK';$f.AcceptButton=$b;$f.Controls.Add($b);$c=New-Object System.Windows.Forms.Button;$c.Text='Anuluj';$c.Width=100;$c.Height=28;$c.Left=($width-130);$c.Top=($height-70);$c.DialogResult='Cancel';$f.CancelButton=$c;$f.Controls.Add($c);" + foregroundPrelude + "if($f.ShowDialog()-ne'OK'-or $g.CurrentRow-eq$null){return $null};return @($g.SelectedRows|Sort-Object Index)};")
	// PowerShell command mode only. showGrid(a,b,c) is ONE array argument, so
	// $f.Text becomes the whole call (title + 720 + scriptblock + Dalej).
	// Recoveries stay reachable even when servers still exist: a virtual
	// first-row pick, not a hidden second dialog after the last server dies.
	sb.WriteString("function showRecoveries(){if(-not $d.Recoveries){return};$rows=showGrid $d.Title $(if($d.Text){$d.Text}else{'Dostępne archiwa odzyskiwania.'}) 760 380 @('ID','Serwer','Stan','Ścieżka') {param($t) foreach($r in $d.Recoveries){$id='@recovery-grace:'+$r.OperationID;if($r.CanDownload){$id='@recovery-download:'+$r.OperationID};[void]$t.Rows.Add($id,$r.ServerName,$r.Status,$r.KitPath)}} $false 'Pobierz';if($rows){$id=[string]$rows[0].Cells['ID'].Value;if($id.StartsWith('@recovery-download:')){$script:answer='download_recovery|'+$id+'|'}}};")
	sb.WriteString("if(-not $d.Servers -or @($d.Servers).Count -eq 0){if($d.Recoveries){showRecoveries}else{$script:answer='close'}}else{$servers=showGrid $d.Title $(if($d.Text){$d.Text}else{'Wybierz serwer.'}) 720 380 @('ID','Serwer','Adres','Strefa') {param($t) foreach($s in $d.Servers){$n=$s.Name;if([string]::IsNullOrWhiteSpace($n)){$n=$s.ID};[void]$t.Rows.Add($s.ID,$n,$s.Address,$s.Realm)};if($d.Recoveries -and @($d.Recoveries).Count -gt 0){[void]$t.Rows.Add('@recoveries','Odzyskiwanie repozytoriów','pobierz archiwa','')}} $false 'Dalej';if($servers){$sid=[string]$servers[0].Cells['ID'].Value;if($sid-eq'@recoveries'){showRecoveries}else{$server=$d.Servers|Where-Object{$_.ID-eq$sid}|Select-Object -First 1;if($server){$name=$server.Name;if([string]::IsNullOrWhiteSpace($name)){$name=$server.ID};$acts=showGrid ('Ustawienia FileES — '+$name) 'Wybierz działanie.' 640 440 @('Działanie') {param($t) foreach($a in $server.Actions){[void]$t.Rows.Add($a.Label)}} $false 'Dalej';if($acts){$label=[string]$acts[0].Cells['Działanie'].Value;$act=$server.Actions|Where-Object{$_.Label-eq$label}|Select-Object -First 1;if($act){$aid=$act.WindowsID;if(-not $act.NeedsFolder){$script:answer=$aid+'|'+$sid+'|'}else{$multi=[bool]$act.Multi;$ok='Wykonaj';if($multi){$ok='Połącz wybrane'};$folders=showGrid 'Ustawienia FileES' 'Wybierz folder dla tej akcji.' 900 500 @('ID','Repozytorium','Ścieżka','Stan','Dostęp','Edycja') {param($t) foreach($f in $act.Folders){[void]$t.Rows.Add($f.ID,$f.Name,$f.Path,$f.State,$f.Access,$f.Editing)}} $multi $ok;if($folders){$ids=@($folders|ForEach-Object{[string]$_.Cells['ID'].Value});$script:answer=$aid+'|'+$sid+'|'+($ids-join ',')}}}}}}}};")
	sb.WriteString("$script:answer")
	return sb.String(), nil
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
		"$t=New-Object System.Data.DataTable;[void]$t.Columns.Add('RealmID');[void]$t.Columns.Add('Gość');[void]$t.Columns.Add('Aktualne uprawnienie');foreach($r in $d.Recipients){$name=$r.Alias;if([string]::IsNullOrWhiteSpace($name)){$name=$r.RealmID};$access='brak';if($r.State-eq'active'-and$r.Access-eq'r'){$access='tylko odczyt'}elseif($r.State-eq'active'-and$r.Access-eq'rw'){$access='odczyt i zapis'};[void]$t.Rows.Add($r.RealmID,$name,$access)};$g.DataSource=$t;$g.Columns['RealmID'].Visible=$false;$f.Controls.Add($g);$script:answer='close';" +
		"function grant($a){if($g.CurrentRow -ne $null){$script:answer=$a+'|'+[string]$g.CurrentRow.Cells['RealmID'].Value;$f.Close()}};" +
		"$r=New-Object System.Windows.Forms.Button;$r.Text='Tylko odczyt';$r.Left=180;$r.Top=438;$r.Width=115;$r.Add_Click({grant 'grant_read'});$f.Controls.Add($r);" +
		"$w=New-Object System.Windows.Forms.Button;$w.Text='Odczyt i zapis';$w.Left=305;$w.Top=438;$w.Width=115;$w.Add_Click({grant 'grant_write'});$f.Controls.Add($w);" +
		"$x=New-Object System.Windows.Forms.Button;$x.Text='Cofnij dostęp';$x.Left=430;$x.Top=438;$x.Width=115;$x.Add_Click({grant 'revoke'});$f.Controls.Add($x);" +
		"$c=New-Object System.Windows.Forms.Button;$c.Text='Anuluj';$c.Left=580;$c.Top=438;$c.Width=100;$c.DialogResult='Cancel';$f.CancelButton=$c;$f.Controls.Add($c);" + foregroundPrelude + "[void]$f.ShowDialog();$script:answer"
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

func (b *WindowsBackend) ShowPublicShares(ctx context.Context, request PublicShareDialogRequest) (PublicShareDialogResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return PublicShareDialogResult{}, NewUnavailable("public_share_dialog", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return PublicShareDialogResult{}, NewOperationalFailure("public_share_dialog", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	script := dpiAwarenessPrelude +
		"Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;" +
		"$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + psString(encoded) + "))|ConvertFrom-Json;" +
		"$f=New-Object System.Windows.Forms.Form;$f.Text=$d.Title;$f.Width=1100;$f.Height=590;$f.StartPosition='CenterScreen';" +
		"$l=New-Object System.Windows.Forms.Label;$l.Text=$d.Text;$l.Left=12;$l.Top=12;$l.Width=1040;$l.Height=44;$f.Controls.Add($l);" +
		"$g=New-Object System.Windows.Forms.DataGridView;$g.Left=12;$g.Top=62;$g.Width=1040;$g.Height=420;$g.ReadOnly=$true;$g.AllowUserToAddRows=$false;$g.SelectionMode='FullRowSelect';$g.MultiSelect=$false;$g.AutoSizeColumnsMode='Fill';" +
		"$t=New-Object System.Data.DataTable;foreach($c in @('ChannelID','Adres','Stan','Folder źródłowy','Odbiorcy','Hasło','Rewizja')){[void]$t.Columns.Add($c)};foreach($r in $d.Shares){[void]$t.Rows.Add($r.ChannelID,$r.Address,$r.State,$r.SourceRoot,$r.Recipients,$r.Password,$r.Revision)};$g.DataSource=$t;if($g.Columns['ChannelID']-ne$null){$g.Columns['ChannelID'].Visible=$false};$f.Controls.Add($g);$script:answer='close';" +
		"function choose($a){if($a-eq'create'){$script:answer='create|';$f.Close()}elseif($g.CurrentRow-ne$null){$script:answer=$a+'|'+[string]$g.CurrentRow.Cells['ChannelID'].Value;$f.Close()}};" +
		"$n=New-Object System.Windows.Forms.Button;$n.Text='Nowe';$n.Left=390;$n.Top=500;$n.Width=100;$n.Add_Click({choose 'create'});$f.Controls.Add($n);" +
		"$e=New-Object System.Windows.Forms.Button;$e.Text='Edytuj';$e.Left=500;$e.Top=500;$e.Width=100;$e.Add_Click({choose 'edit'});$f.Controls.Add($e);" +
		"$r=New-Object System.Windows.Forms.Button;$r.Text='Cofnij';$r.Left=610;$r.Top=500;$r.Width=100;$r.Add_Click({choose 'revoke'});$f.Controls.Add($r);" +
		"$x=New-Object System.Windows.Forms.Button;$x.Text='Usuń';$x.Left=720;$x.Top=500;$x.Width=100;$x.Add_Click({choose 'delete'});$f.Controls.Add($x);" +
		"$c=New-Object System.Windows.Forms.Button;$c.Text='Zamknij';$c.Left=900;$c.Top=500;$c.Width=100;$c.DialogResult='Cancel';$f.CancelButton=$c;$f.Controls.Add($c);" + foregroundPrelude + "[void]$f.ShowDialog();$script:answer"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctx.Err() != nil {
			return PublicShareDialogResult{}, ctx.Err()
		}
		return PublicShareDialogResult{}, NewOperationalFailure("public_share_dialog", err)
	}
	action, channelID, ok := strings.Cut(strings.TrimSpace(string(output)), "|")
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

func (b *WindowsBackend) ShowUploadChannels(ctx context.Context, request UploadChannelDialogRequest) (UploadChannelDialogResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return UploadChannelDialogResult{}, NewUnavailable("upload_channel_dialog", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return UploadChannelDialogResult{}, NewOperationalFailure("upload_channel_dialog", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	script := dpiAwarenessPrelude +
		"Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;" +
		"$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" + psString(encoded) + "))|ConvertFrom-Json;" +
		"$f=New-Object System.Windows.Forms.Form;$f.Text=$d.Title;$f.Width=980;$f.Height=590;$f.StartPosition='CenterScreen';" +
		"$l=New-Object System.Windows.Forms.Label;$l.Text=$d.Text;$l.Left=12;$l.Top=12;$l.Width=940;$l.Height=44;$f.Controls.Add($l);" +
		"$g=New-Object System.Windows.Forms.DataGridView;$g.Left=12;$g.Top=62;$g.Width=940;$g.Height=420;$g.ReadOnly=$true;$g.AllowUserToAddRows=$false;$g.SelectionMode='FullRowSelect';$g.MultiSelect=$false;$g.AutoSizeColumnsMode='Fill';" +
		"$t=New-Object System.Data.DataTable;foreach($c in @('ChannelID','Adres','Stan','Wnoszący')){[void]$t.Columns.Add($c)};foreach($r in $d.Channels){[void]$t.Rows.Add($r.ChannelID,$r.Address,$r.State,$r.Recipients)};$g.DataSource=$t;if($g.Columns['ChannelID']-ne$null){$g.Columns['ChannelID'].Visible=$false};$f.Controls.Add($g);$script:answer='close';" +
		"function choose($a){if($a-eq'create'){$script:answer='create|';$f.Close()}elseif($g.CurrentRow-ne$null){$script:answer=$a+'|'+[string]$g.CurrentRow.Cells['ChannelID'].Value;$f.Close()}};" +
		"$n=New-Object System.Windows.Forms.Button;$n.Text='Nowa półka';$n.Left=270;$n.Top=500;$n.Width=110;$n.Add_Click({choose 'create'});$f.Controls.Add($n);" +
		"$e=New-Object System.Windows.Forms.Button;$e.Text='Edytuj';$e.Left=390;$e.Top=500;$e.Width=100;$e.Add_Click({choose 'edit'});$f.Controls.Add($e);" +
		"$r=New-Object System.Windows.Forms.Button;$r.Text='Cofnij';$r.Left=500;$r.Top=500;$r.Width=100;$r.Add_Click({choose 'revoke'});$f.Controls.Add($r);" +
		"$x=New-Object System.Windows.Forms.Button;$x.Text='Usuń';$x.Left=610;$x.Top=500;$x.Width=100;$x.Add_Click({choose 'delete'});$f.Controls.Add($x);" +
		"$c=New-Object System.Windows.Forms.Button;$c.Text='Zamknij';$c.Left=790;$c.Top=500;$c.Width=100;$c.DialogResult='Cancel';$f.CancelButton=$c;$f.Controls.Add($c);" + foregroundPrelude + "[void]$f.ShowDialog();$script:answer"
	output, err := b.runner.Output(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script)
	if err != nil {
		if ctx.Err() != nil {
			return UploadChannelDialogResult{}, ctx.Err()
		}
		return UploadChannelDialogResult{}, NewOperationalFailure("upload_channel_dialog", err)
	}
	action, channelID, ok := strings.Cut(strings.TrimSpace(string(output)), "|")
	if !ok {
		return UploadChannelDialogResult{Action: UploadChannelDialogClose}, nil
	}
	result := UploadChannelDialogResult{ChannelID: channelID}
	switch action {
	case "create":
		result.Action = UploadChannelDialogCreate
	case "edit":
		result.Action = UploadChannelDialogEdit
	case "revoke":
		result.Action = UploadChannelDialogRevoke
	case "delete":
		result.Action = UploadChannelDialogDelete
	default:
		result.Action = UploadChannelDialogClose
	}
	return result, nil
}

func (b *WindowsBackend) ShowRealmVisibility(ctx context.Context, request RealmVisibilityDialogRequest) (RealmVisibilityDialogResult, error) {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return RealmVisibilityDialogResult{}, NewUnavailable("realm_visibility_dialog", err)
	}
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;" + foregroundOwnerPrelude + "$r=[System.Windows.Forms.MessageBox]::Show($owner," + psString(request.Text) + "," + psString(request.Title) + ",'YesNoCancel','Question');if($r-eq'Yes'){'listed'}elseif($r-eq'No'){'hidden'}else{'close'}"
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
	confirmText := strings.TrimSpace(request.ConfirmText)
	if confirmText == "" {
		confirmText = "Potwierdź"
	}
	cancelText := strings.TrimSpace(request.CancelText)
	if cancelText == "" {
		cancelText = "Anuluj"
	}
	// A stock MessageBox cannot honour ConfirmText/CancelText.  Use the same
	// short-lived WinForms boundary as the richer dialogs so destructive
	// actions can name their effect and always expose a literal, default-safe
	// cancellation path.  The answer remains a single line on stdout.
	script := dpiAwarenessPrelude + "Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;" +
		"$f=New-Object Windows.Forms.Form;$f.Text=" + psString(request.Title) + ";$f.Width=680;$f.Height=285;$f.StartPosition='CenterScreen';$f.FormBorderStyle='FixedDialog';$f.MaximizeBox=$false;$f.MinimizeBox=$false;$f.ShowInTaskbar=$false;" +
		"$l=New-Object Windows.Forms.Label;$l.Text=" + psString(request.Text) + ";$l.Left=20;$l.Top=20;$l.Width=625;$l.Height=155;$l.Font=New-Object Drawing.Font('Segoe UI',10);$f.Controls.Add($l);" +
		"$ok=New-Object Windows.Forms.Button;$ok.Text=" + psString(confirmText) + ";$ok.Left=405;$ok.Top=195;$ok.Width=115;$ok.Height=32;$ok.DialogResult='OK';$f.Controls.Add($ok);" +
		"$cancel=New-Object Windows.Forms.Button;$cancel.Text=" + psString(cancelText) + ";$cancel.Left=530;$cancel.Top=195;$cancel.Width=115;$cancel.Height=32;$cancel.DialogResult='Cancel';$f.Controls.Add($cancel);" +
		"$f.AcceptButton=$cancel;$f.CancelButton=$cancel;" + foregroundPrelude +
		"$cancel.Select();$d=$f.ShowDialog();if($d-eq'OK'){'yes'}else{'no'}"
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
		foregroundPrelude + "$d=$f.ShowDialog();if($d-eq'OK'){'ok|'+$r.Checked+'|'+$o.Checked}else{'cancel|false|false'}"
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
	sb.WriteString("$close=New-Object System.Windows.Forms.Button;$close.Text='Zamknij';$close.Width=[int](100*$s);$close.Height=[int](28*$s);$close.Left=[int](1010*$s);$close.Top=[int](540*$s);$close.DialogResult='Cancel';$f.CancelButton=$close;$f.Controls.Add($close);" + foregroundPrelude + "[void]$f.ShowDialog();$script:answer")
	return sb.String(), nil
}

// ShowJournal displays the shared activity/error chronology. Error rows are
// rendered in bold and dark red; this emphasis is intentionally native rather
// than encoded into the journal data.
func (b *WindowsBackend) ShowJournal(ctx context.Context, request JournalDialogRequest) error {
	command, err := b.runner.LookPath("powershell.exe")
	if err != nil {
		return NewUnavailable("journal_dialog", err)
	}
	script, err := buildJournalDialogScript(request)
	if err != nil {
		return NewOperationalFailure("journal_dialog", err)
	}
	if err := b.runner.Run(ctx, command, "-NoProfile", "-NonInteractive", "-Sta", "-WindowStyle", "Hidden", "-Command", script); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return NewOperationalFailure("journal_dialog", err)
	}
	return nil
}

func buildJournalDialogScript(request JournalDialogRequest) (string, error) {
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
	sb.WriteString("$f=New-Object System.Windows.Forms.Form;$f.Text=$d.Title;$f.Width=[int](1220*$s);$f.Height=[int](700*$s);$f.StartPosition='CenterScreen';")
	sb.WriteString("$l=New-Object System.Windows.Forms.Label;$l.AutoSize=$false;$l.Left=[int](12*$s);$l.Top=[int](12*$s);$l.Width=[int](1170*$s);$l.Height=[int](36*$s);$l.Text=$d.Text;$f.Controls.Add($l);")
	sb.WriteString("$g=New-Object System.Windows.Forms.DataGridView;$g.Left=[int](12*$s);$g.Top=[int](54*$s);$g.Width=[int](1170*$s);$g.Height=[int](550*$s);$g.ReadOnly=$true;$g.AllowUserToAddRows=$false;$g.AllowUserToDeleteRows=$false;$g.SelectionMode='FullRowSelect';$g.MultiSelect=$false;$g.AutoSizeColumnsMode='Fill';$g.AutoSizeRowsMode='AllCells';$g.DefaultCellStyle.WrapMode='True';")
	sb.WriteString("$t=New-Object System.Data.DataTable;[void]$t.Columns.Add('Timestamp');[void]$t.Columns.Add('Repository');[void]$t.Columns.Add('Summary');[void]$t.Columns.Add('Details');[void]$t.Columns.Add('Severity');[void]$t.Columns.Add('Emphasized',[bool]);foreach($r in $d.Rows){[void]$t.Rows.Add($r.Timestamp,$r.Repository,$r.Summary,$r.Details,$r.Severity,[bool]$r.Emphasized)};$g.DataSource=$t;")
	sb.WriteString("$g.Columns['Timestamp'].HeaderText='Czas';$g.Columns['Timestamp'].FillWeight=22;$g.Columns['Repository'].HeaderText='Zakres';$g.Columns['Repository'].FillWeight=24;$g.Columns['Summary'].HeaderText='Wpis';$g.Columns['Summary'].FillWeight=58;$g.Columns['Details'].HeaderText='Szczegóły';$g.Columns['Details'].FillWeight=42;$g.Columns['Severity'].Visible=$false;$g.Columns['Emphasized'].Visible=$false;")
	sb.WriteString("$errorFont=New-Object Drawing.Font($g.Font,[Drawing.FontStyle]::Bold);foreach($row in $g.Rows){if([bool]$row.Cells['Emphasized'].Value){$row.DefaultCellStyle.Font=$errorFont;$row.DefaultCellStyle.ForeColor=[Drawing.Color]::Firebrick}};$f.Controls.Add($g);")
	sb.WriteString("$close=New-Object System.Windows.Forms.Button;$close.Text='Zamknij';$close.Width=[int](100*$s);$close.Height=[int](28*$s);$close.Left=[int](1082*$s);$close.Top=[int](616*$s);$close.DialogResult='Cancel';$f.CancelButton=$close;$f.Controls.Add($close);" + foregroundPrelude + "[void]$f.ShowDialog();$errorFont.Dispose()")
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
	if request.Default != "" {
		sb.WriteString("$t.Text=" + psString(request.Default) + ";")
	}
	if request.Placeholder != "" {
		// PlaceholderText exists from .NET Framework 4.7.2, which Windows 11
		// comfortably exceeds, but the property is probed rather than assumed:
		// setting an absent property is a terminating error in PowerShell, and
		// losing a hint is worth less than losing the whole dialog.
		sb.WriteString("if($t.PSObject.Properties['PlaceholderText']){$t.PlaceholderText=" + psString(request.Placeholder) + "};")
	}
	if request.Secret {
		sb.WriteString("$t.UseSystemPasswordChar=$true;")
	}
	sb.WriteString("$f.Controls.Add($t);$b=New-Object System.Windows.Forms.Button;$b.Text='OK';$b.Width=[int](75*$s);$b.Height=[int](23*$s);$b.Left=[int](412*$s);$b.Top=[int](82*$s);$b.DialogResult='OK';$f.AcceptButton=$b;$f.Controls.Add($b);")
	sb.WriteString(foregroundPrelude + "if($f.ShowDialog()-eq[System.Windows.Forms.DialogResult]::OK){$t.Text}else{exit 1}")
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
