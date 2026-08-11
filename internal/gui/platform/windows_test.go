//go:build windows

package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type windowsCommandCall struct {
	name   string
	args   []string
	method string
}

type fakeWindowsRunner struct {
	mu     sync.Mutex
	paths  map[string]string
	calls  []windowsCommandCall
	output func(context.Context, string, []string) ([]byte, error)
	run    func(context.Context, string, []string) error
	start  func(context.Context, string, []string) error
}

func (f *fakeWindowsRunner) LookPath(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path := f.paths[name]; path != "" {
		return path, nil
	}
	return "", os.ErrNotExist
}

func (f *fakeWindowsRunner) Start(ctx context.Context, name string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, windowsCommandCall{name: name, args: append([]string(nil), args...), method: "start"})
	fn := f.start
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, name, args)
	}
	return nil
}

func (f *fakeWindowsRunner) Run(ctx context.Context, name string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, windowsCommandCall{name: name, args: append([]string(nil), args...), method: "run"})
	fn := f.run
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, name, args)
	}
	return nil
}

func (f *fakeWindowsRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, windowsCommandCall{name: name, args: append([]string(nil), args...), method: "output"})
	fn := f.output
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, name, args)
	}
	return nil, nil
}

func (f *fakeWindowsRunner) Calls() []windowsCommandCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]windowsCommandCall(nil), f.calls...)
}

type fakeWindowsExitError int

func (e fakeWindowsExitError) Error() string { return "exit" }
func (e fakeWindowsExitError) ExitCode() int { return int(e) }

func newTestWindowsBackend(runner *fakeWindowsRunner, now func() time.Time) *WindowsBackend {
	if runner.paths == nil {
		runner.paths = map[string]string{
			"explorer.exe":   `C:\Windows\explorer.exe`,
			"powershell.exe": `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		}
	}
	return newWindowsBackend(runner, newFakeWindowsAutostartStore(), now, "ATMProjekt.FileES")
}

type fakeWindowsAutostartStore struct {
	mu     sync.Mutex
	values map[string]string
	err    error
}

func newFakeWindowsAutostartStore() *fakeWindowsAutostartStore {
	return &fakeWindowsAutostartStore{values: make(map[string]string)}
}

func (s *fakeWindowsAutostartStore) Value(name string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", false, s.err
	}
	value, ok := s.values[name]
	return value, ok, nil
}

func (s *fakeWindowsAutostartStore) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.values[name] = value
	return nil
}

func (s *fakeWindowsAutostartStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	delete(s.values, name)
	return nil
}

func (s *fakeWindowsAutostartStore) StoredValue(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[name]
}

func TestWindowsOpenFolderCallsExplorer(t *testing.T) {
	runner := &fakeWindowsRunner{}
	backend := newTestWindowsBackend(runner, time.Now)
	if err := backend.OpenFolder(context.Background(), `C:\projects\work`); err != nil {
		t.Fatal(err)
	}
	calls := runner.Calls()
	if len(calls) != 1 || calls[0].name != `C:\Windows\explorer.exe` || calls[0].method != "start" ||
		len(calls[0].args) != 1 || calls[0].args[0] != `C:\projects\work` {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestHideConsoleWindowOnlyAffectsPowerShell(t *testing.T) {
	powerShell := exec.Command(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`)
	hideConsoleWindow(powerShell)
	if powerShell.SysProcAttr == nil || powerShell.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatal("PowerShell must start with no console allocated")
	}
	if powerShell.SysProcAttr.HideWindow {
		t.Fatal("PowerShell must not use HideWindow: it overrides the first ShowWindow call any dialog the script opens makes, forcing it to start minimized")
	}

	explorer := exec.Command(`C:\Windows\explorer.exe`)
	hideConsoleWindow(explorer)
	if explorer.SysProcAttr != nil {
		t.Fatal("Explorer must keep its normal visible window behavior")
	}
}

func TestWindowsOpenFolderReportsUnavailableAndStartFailure(t *testing.T) {
	missing := &fakeWindowsRunner{paths: map[string]string{}}
	backend := newWindowsBackend(missing, newFakeWindowsAutostartStore(), time.Now, "ATMProjekt.FileES")
	if err := backend.OpenFolder(context.Background(), `C:\wc`); !IsFailure(err, FailureUnavailable) {
		t.Fatalf("missing explorer error = %v", err)
	}

	runner := &fakeWindowsRunner{
		start: func(context.Context, string, []string) error { return errors.New("access denied") },
	}
	backend = newTestWindowsBackend(runner, time.Now)
	if err := backend.OpenFolder(context.Background(), `C:\wc`); !IsFailure(err, FailureOperational) {
		t.Fatalf("start failure = %v", err)
	}
}

func TestWindowsOpenFolderRejectsRelativePath(t *testing.T) {
	backend := newTestWindowsBackend(&fakeWindowsRunner{}, time.Now)
	if err := backend.OpenFolder(context.Background(), `relative\path`); !IsFailure(err, FailureOperational) {
		t.Fatalf("relative path error = %v", err)
	}
}

func TestWindowsPickerInvokesPowerShellAndValidatesSelection(t *testing.T) {
	runner := &fakeWindowsRunner{
		output: func(_ context.Context, _ string, args []string) ([]byte, error) {
			return []byte(`C:\wc\a.dwg` + "\n" + `C:\wc\sub\b.dwg` + "\n"), nil
		},
	}
	backend := newTestWindowsBackend(runner, time.Now)
	result, err := backend.PickFiles(context.Background(), PickFilesRequest{
		Title:         "Zablokuj",
		Root:          `C:\wc`,
		InitialDir:    `C:\wc\sub`,
		AllowMultiple: true,
	})
	if err != nil || result.Cancelled || len(result.Paths) != 2 {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}
	calls := runner.Calls()
	if len(calls) != 1 || calls[0].method != "output" ||
		!strings.HasSuffix(strings.ToLower(calls[0].name), `\powershell.exe`) {
		t.Fatalf("calls = %#v", calls)
	}
	script := calls[0].args[len(calls[0].args)-1]
	if !strings.HasPrefix(script, dpiAwarenessPrelude) {
		t.Fatalf("picker must set DPI awareness before WinForms: %s", script)
	}
	for _, expected := range []string{
		"OpenFileDialog",
		"Multiselect=$true",
		`C:\wc\sub`,
		"Zablokuj",
		"FileNames",
	} {
		if !contains(script, expected) {
			t.Errorf("picker script missing %q", expected)
		}
	}
	if args := strings.Join(calls[0].args, " "); !strings.Contains(args, "-Sta") || !strings.Contains(args, "-WindowStyle Hidden") {
		t.Fatalf("PowerShell picker args = %#v", calls[0].args)
	}
}

func TestWindowsFolderPickerUsesFolderBrowserDialog(t *testing.T) {
	runner := &fakeWindowsRunner{output: func(context.Context, string, []string) ([]byte, error) { return []byte(`C:\data\projekt`), nil }}
	backend := newTestWindowsBackend(runner, time.Now)
	result, err := backend.PickFolder(context.Background(), PickFolderRequest{Title: "Folder", InitialDir: `C:\data`})
	if err != nil || result.Path != `C:\data\projekt` {
		t.Fatalf("PickFolder() = %#v, %v", result, err)
	}
	args := strings.Join(runner.Calls()[0].args, " ")
	if !strings.HasPrefix(runner.Calls()[0].args[len(runner.Calls()[0].args)-1], dpiAwarenessPrelude) {
		t.Fatalf("folder picker must set DPI awareness before WinForms: %s", args)
	}
	if !strings.Contains(args, "FolderBrowserDialog") {
		t.Fatalf("script = %s", args)
	}
}

func TestWindowsPickerCancellationAndOutsideRoot(t *testing.T) {
	runner := &fakeWindowsRunner{
		output: func(_ context.Context, _ string, _ []string) ([]byte, error) {
			return nil, fakeWindowsExitError(1)
		},
	}
	backend := newTestWindowsBackend(runner, time.Now)
	result, err := backend.PickFiles(context.Background(), PickFilesRequest{Root: `C:\wc`})
	if err != nil || !result.Cancelled {
		t.Fatalf("cancelled PickFiles() = %#v, %v", result, err)
	}

	runner.output = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte(`C:\other\secret.dwg` + "\n"), nil
	}
	if _, err := backend.PickFiles(context.Background(), PickFilesRequest{Root: `C:\wc`}); !IsFailure(err, FailureOperational) {
		t.Fatalf("outside-root error = %v", err)
	}
}

func TestWindowsNotificationsRateLimitByGroup(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	runner := &fakeWindowsRunner{}
	backend := newTestWindowsBackend(runner, func() time.Time { return now })
	n := Notification{Group: "repo-a", Title: "Offline", Body: "Brak sieci", Urgency: UrgencyCritical}
	if err := backend.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := backend.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("rate-limited calls = %d, want 1", got)
	}

	now = now.Add(defaultWindowsNotifInterval)
	if err := backend.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 2 {
		t.Fatalf("calls after interval = %d, want 2", got)
	}
}

func TestWindowsNotificationFailureReleasesRateLimit(t *testing.T) {
	runner := &fakeWindowsRunner{
		run: func(_ context.Context, _ string, _ []string) error { return errors.New("powershell error") },
	}
	backend := newTestWindowsBackend(runner, time.Now)
	n := Notification{Group: "repo", Title: "Offline"}
	for i := 0; i < 2; i++ {
		if err := backend.Notify(context.Background(), n); !IsFailure(err, FailureOperational) {
			t.Fatalf("Notify() error = %v", err)
		}
	}
	if got := len(runner.Calls()); got != 2 {
		t.Fatalf("calls after failures = %d, want 2", got)
	}
}

func TestWindowsNotificationScriptContainsTag(t *testing.T) {
	var capturedScript string
	runner := &fakeWindowsRunner{
		run: func(_ context.Context, _ string, args []string) error {
			capturedScript = args[len(args)-1]
			return nil
		},
	}
	backend := newTestWindowsBackend(runner, time.Now)
	n := Notification{Group: "repo-sync", Title: "Synchronizacja", Body: "Pliki zsynchronizowane"}
	if err := backend.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	tag := windowsToastTag("repo-sync")
	for _, expected := range []string{"ToastGeneric", tag, "ATMProjekt.FileES", "Synchronizacja", "Pliki zsynchronizowane"} {
		if !contains(capturedScript, expected) {
			t.Errorf("toast script missing %q: %s", expected, capturedScript)
		}
	}
	if len(tag) != 16 {
		t.Fatalf("toast tag = %q, want 16 characters", tag)
	}
}

func TestWindowsNotificationWithoutGroupOmitsTag(t *testing.T) {
	var capturedScript string
	runner := &fakeWindowsRunner{
		run: func(_ context.Context, _ string, args []string) error {
			capturedScript = args[len(args)-1]
			return nil
		},
	}
	backend := newTestWindowsBackend(runner, time.Now)
	if err := backend.Notify(context.Background(), Notification{Title: "Gotowe"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(capturedScript, "$toast.Tag=") {
		t.Fatalf("ungrouped notification has a tag: %s", capturedScript)
	}
}

func TestWindowsNotificationRequiresConfiguredAUMID(t *testing.T) {
	runner := &fakeWindowsRunner{
		paths: map[string]string{"powershell.exe": `C:\Windows\powershell.exe`},
	}
	backend := newWindowsBackend(runner, newFakeWindowsAutostartStore(), time.Now, "")
	err := backend.Notify(context.Background(), Notification{Title: "Gotowe"})
	if !IsFailure(err, FailureUnavailable) {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("PowerShell called without AUMID: %#v", runner.Calls())
	}
}

func TestWindowsNotificationRejectsInvalidAUMID(t *testing.T) {
	runner := &fakeWindowsRunner{
		paths: map[string]string{"powershell.exe": `C:\Windows\powershell.exe`},
	}
	backend := newWindowsBackend(runner, newFakeWindowsAutostartStore(), time.Now, "invalid AUMID")
	err := backend.Notify(context.Background(), Notification{Title: "Gotowe"})
	if !IsFailure(err, FailureOperational) {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatalf("PowerShell called with invalid AUMID: %#v", runner.Calls())
	}
}

func TestWindowsToastTagIsBoundedForLongRepoID(t *testing.T) {
	tag := windowsToastTag(strings.Repeat("repo-", 100))
	if len(tag) != 16 {
		t.Fatalf("tag = %q, length = %d", tag, len(tag))
	}
}

func TestWindowsPsStringEscapesSingleQuotes(t *testing.T) {
	tests := []struct{ in, want string }{
		{`plain`, `'plain'`},
		{`O'Brien`, `'O''Brien'`},
		{`it's a "test"`, `'it''s a "test"'`},
	}
	for _, tt := range tests {
		if got := psString(tt.in); got != tt.want {
			t.Errorf("psString(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWindowsAutostartLifecycle(t *testing.T) {
	ctx := context.Background()
	spec := AutostartSpec{
		ID:         "filees-test-autostart",
		Name:       "FileES Test",
		Executable: `C:\Program Files\FileES\filees-gui.exe`,
		Args:       []string{"--daemon-socket", `\\.\pipe\filees`},
	}
	store := newFakeWindowsAutostartStore()
	backend := newWindowsBackend(&fakeWindowsRunner{}, store, time.Now, "ATMProjekt.FileES")

	state, err := backend.AutostartStatus(ctx, spec)
	if err != nil || state.Enabled {
		t.Fatalf("initial status = %#v, %v", state, err)
	}

	if err := backend.SetAutostart(ctx, spec, true); err != nil {
		t.Fatalf("enable autostart: %v", err)
	}
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || !state.Enabled || !state.Current {
		t.Fatalf("enabled status = %#v, %v", state, err)
	}
	execLine := store.StoredValue(spec.ID)
	if !strings.Contains(execLine, "filees-gui.exe") || !strings.Contains(execLine, "filees") {
		t.Fatalf("autostart command line = %q", execLine)
	}
	store.values[spec.ID] = `C:\old\filees-gui.exe`
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || !state.Enabled || state.Current {
		t.Fatalf("stale status = %#v, %v", state, err)
	}
	if err := backend.SetAutostart(ctx, spec, true); err != nil {
		t.Fatalf("repair autostart: %v", err)
	}

	if err := backend.SetAutostart(ctx, spec, false); err != nil {
		t.Fatalf("disable autostart: %v", err)
	}
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || state.Enabled {
		t.Fatalf("disabled status = %#v, %v", state, err)
	}
}

func TestWindowsAutostartStoreFailureIsOperational(t *testing.T) {
	store := newFakeWindowsAutostartStore()
	store.err = errors.New("registry denied")
	backend := newWindowsBackend(&fakeWindowsRunner{}, store, time.Now, "ATMProjekt.FileES")
	spec := AutostartSpec{ID: "filees", Name: "FileES", Executable: `C:\FileES\filees-gui.exe`}
	if _, err := backend.AutostartStatus(context.Background(), spec); !IsFailure(err, FailureOperational) {
		t.Fatalf("AutostartStatus() error = %v", err)
	}
	if err := backend.SetAutostart(context.Background(), spec, true); !IsFailure(err, FailureOperational) {
		t.Fatalf("SetAutostart() error = %v", err)
	}
}

func TestWindowsExecLineHandlesQuotesAndTrailingBackslashes(t *testing.T) {
	spec := AutostartSpec{
		Executable: `C:\Program Files\FileES\filees-gui.exe`,
		Args:       []string{`C:\path with spaces\`, `quote"inside`, ""},
	}
	got := buildWindowsExecLine(spec)
	want := windows.ComposeCommandLine(append([]string{filepath.Clean(spec.Executable)}, spec.Args...))
	if got != want {
		t.Fatalf("command line = %q, want %q", got, want)
	}
}

func TestWindowsAutostartRejectsUnsafeInput(t *testing.T) {
	backend := newTestWindowsBackend(&fakeWindowsRunner{}, time.Now)
	if err := backend.SetAutostart(context.Background(), AutostartSpec{ID: "../escape"}, true); !IsFailure(err, FailureOperational) {
		t.Fatalf("unsafe ID error = %v", err)
	}
	if err := backend.SetAutostart(context.Background(), AutostartSpec{ID: "filees", Name: "FileES", Executable: `relative\path.exe`}, true); !IsFailure(err, FailureOperational) {
		t.Fatalf("relative executable error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.AutostartStatus(ctx, AutostartSpec{ID: "filees"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled AutostartStatus() = %v", err)
	}
}

func TestWindowsConcurrentNotificationsSameGroupSendOnce(t *testing.T) {
	runner := &fakeWindowsRunner{}
	backend := newTestWindowsBackend(runner, func() time.Time {
		return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	})
	const calls = 20
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			if err := backend.Notify(context.Background(), Notification{Group: "repo", Title: "Offline"}); err != nil {
				t.Errorf("Notify() = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("notification commands = %d, want 1", got)
	}
}

func contains(s, substr string) bool { return strings.Contains(s, substr) }

func TestWindowsJournalScriptUsesNativeErrorEmphasis(t *testing.T) {
	script, err := buildJournalDialogScript(JournalDialogRequest{Rows: []JournalDialogRow{{Summary: "⚠ BŁĄD", Emphasized: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"DataGridView", "[Drawing.FontStyle]::Bold", "[Drawing.Color]::Firebrick", "Cells['Emphasized']"} {
		if !strings.Contains(script, wanted) {
			t.Errorf("journal script missing %q", wanted)
		}
	}
}

// TestWindowsSettingsDialogResolvesAnswers pins the reply contract between
// buildSettingsDialogScript's act() helper and ShowSettings' parser. The
// recovery cases are the ones that regressed: a recovery row's ServerID is
// "@recovery-download:<id>", so while the reply was ":"-separated it split
// into four fields, failed the len()==3 guard and resolved to Close -- the
// archive-download button ran the dialog and then did nothing at all. The
// "@recovery-grace:" row must still resolve to Close, because its archive is
// not downloadable yet.
func TestWindowsSettingsDialogResolvesAnswers(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   SettingsDialogResult
	}{
		{"download recovery with colon in id", "download_recovery|@recovery-download:op-1|\r\n", SettingsDialogResult{Action: SettingsDialogDownloadRecovery, OperationID: "op-1"}},
		{"recovery still in grace period", "download_recovery|@recovery-grace:op-2|\r\n", SettingsDialogResult{Action: SettingsDialogClose, ServerID: "@recovery-grace:op-2"}},
		{"detach folder", "detach|biuro|repo-1\r\n", SettingsDialogResult{Action: SettingsDialogDetachFolder, ServerID: "biuro", RepoID: "repo-1"}},
		{"delete repository", "delete|biuro|repo-1\r\n", SettingsDialogResult{Action: SettingsDialogDeleteRepo, ServerID: "biuro", RepoID: "repo-1"}},
		{"load dump", "load_dump|biuro|repo-1\r\n", SettingsDialogResult{Action: SettingsDialogLoadDump, ServerID: "biuro", RepoID: "repo-1"}},
		{"manage grants", "manage_grants|biuro|repo-1\r\n", SettingsDialogResult{Action: SettingsDialogManageGrants, ServerID: "biuro", RepoID: "repo-1"}},
		{"realm visibility", "realm_visibility|biuro|\r\n", SettingsDialogResult{Action: SettingsDialogRealmVisibility, ServerID: "biuro"}},
		{"add folder without a selected repo", "add|biuro|\r\n", SettingsDialogResult{Action: SettingsDialogAddFolder, ServerID: "biuro"}},
		{"connect multiple repositories", "connect|biuro|repo-1,repo-2\r\n", SettingsDialogResult{Action: SettingsDialogConnectRepos, ServerID: "biuro", RepoIDs: []string{"repo-1", "repo-2"}}},
		{"locate moved working copy", "locate|biuro|repo-1\r\n", SettingsDialogResult{Action: SettingsDialogLocateFolder, ServerID: "biuro", RepoID: "repo-1"}},
		{"deactivate client", "deactivate|biuro|\r\n", SettingsDialogResult{Action: SettingsDialogDetachServer, ServerID: "biuro"}},
		{"remove realm", "remove_realm|biuro|\r\n", SettingsDialogResult{Action: SettingsDialogRemoveRealm, ServerID: "biuro"}},
		{"closed without choosing", "close\r\n", SettingsDialogResult{Action: SettingsDialogClose}},
		{"unknown action", "nonsense|biuro|repo-1\r\n", SettingsDialogResult{Action: SettingsDialogClose, ServerID: "biuro", RepoID: "repo-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeWindowsRunner{output: func(context.Context, string, []string) ([]byte, error) {
				return []byte(test.answer), nil
			}}
			backend := newTestWindowsBackend(runner, time.Now)
			got, err := backend.ShowSettings(context.Background(), SettingsDialogRequest{Title: "Ustawienia FileES"})
			if err != nil {
				t.Fatalf("ShowSettings: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ShowSettings(%q) = %#v, want %#v", test.answer, got, test.want)
			}
		})
	}
}

// settingsPayload decodes the base64/JSON row payload the settings script
// embeds, so a test can assert on the data the dialog is driven by without
// having to render WinForms.
func settingsPayload(t *testing.T, script string) struct {
	Title, Text string
	Rows        []map[string]any
} {
	t.Helper()
	const marker = "FromBase64String('"
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatal("no base64 payload in generated script")
	}
	rest := script[start+len(marker):]
	end := strings.Index(rest, "'")
	if end < 0 {
		t.Fatal("unterminated base64 payload")
	}
	raw, err := base64.StdEncoding.DecodeString(rest[:end])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var decoded struct {
		Title, Text string
		Rows        []map[string]any
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return decoded
}

// TestWindowsSettingsDialogGatesEveryButtonPerRow pins the capability matrix
// the dialog's button bar is driven by. Windows shows one permanent button bar
// rather than linux.go's per-selection action list, so every button -- not
// just the two that already had it -- needs a per-row boolean, and a recovery
// row (an operation ID, with no server or folder behind it) must not enable
// any folder or server action.
//
// Verified live against the rendered dialog: with these rows, row 0 enables
// everything except the archive download, row 1 keeps only the server-level
// actions, and the recovery row enables the archive download alone.
func TestWindowsSettingsDialogGatesEveryButtonPerRow(t *testing.T) {
	script, err := buildSettingsDialogScript(SettingsDialogRequest{
		Title: "Ustawienia FileES",
		Servers: []SettingsServer{{
			ID: "biuro", Name: "Biuro", CanSetRealmVisibility: true, CanAddFolder: true,
			Folders: []SettingsFolder{
				{ID: "repo-1", Name: "Rysunki", CanManageGrants: true, CanLocate: true, CanDetach: true, CanDelete: true, CanLoadDump: true},
				{ID: "repo-2", Name: "Obce", CanConnect: true},
			},
		}, {ID: "readonly", Name: "Audyt"}},
		Recoveries: []SettingsRecovery{{OperationID: "op-1", ServerName: "Biuro", Status: "gotowe", CanDownload: true}},
	})
	if err != nil {
		t.Fatalf("buildSettingsDialogScript: %v", err)
	}

	// Every button must be backed by a hidden capability column, otherwise it
	// would render permanently enabled.
	for _, button := range settingsButtons {
		if !contains(script, "$btns['"+button.capability+"']=$b") {
			t.Errorf("button %q is not registered under capability %q", button.action, button.capability)
		}
	}
	// CurrentCellChanged gates single-row actions. SelectionChanged is also
	// required so Connect follows the connectable subset of a mixed selection.
	if !contains(script, "$g.Add_CurrentCellChanged({updateButtons})") {
		t.Error("button gating is not wired to CurrentCellChanged")
	}
	if !contains(script, "$g.Add_SelectionChanged({updateButtons})") {
		t.Error("multi-row Connect gating is not wired to SelectionChanged")
	}
	if !contains(script, "$g.MultiSelect=$true") || !contains(script, "$ids-join ','") {
		t.Error("settings dialog does not return all selected repositories for Connect")
	}
	if !contains(script, "$g.SelectedRows|Where-Object{[string]$_.Cells['CanConnect'].Value-eq'True'}|Sort-Object Index") {
		t.Error("Connect does not exclude already-attached rows from a mixed selection")
	}
	if !contains(script, "$g.SelectedRows|Where-Object{[string]$_.Cells[$k].Value-eq'True'}") {
		t.Error("Connect button is not enabled for the connectable subset of a mixed selection")
	}

	rows := settingsPayload(t, script).Rows
	want := []struct {
		folder string
		caps   map[string]bool
	}{
		{"Rysunki", map[string]bool{"CanVisibility": true, "CanGrants": true, "CanAdd": true, "CanConnect": false, "CanLocate": true, "CanDetach": true, "CanDelete": true, "CanLoadDump": true, "CanDeactivate": true, "CanRemoveRealm": true, "CanDownloadRecovery": false}},
		{"Obce", map[string]bool{"CanVisibility": true, "CanGrants": false, "CanAdd": true, "CanConnect": true, "CanLocate": false, "CanDetach": false, "CanDelete": false, "CanLoadDump": false, "CanDeactivate": true, "CanRemoveRealm": true, "CanDownloadRecovery": false}},
		// A server that offers neither realm visibility nor folder creation
		// (e.g. a read-only client role) keeps only the two lifecycle actions.
		{"Brak folderów", map[string]bool{"CanVisibility": false, "CanGrants": false, "CanAdd": false, "CanConnect": false, "CanLocate": false, "CanDetach": false, "CanDelete": false, "CanLoadDump": false, "CanDeactivate": true, "CanRemoveRealm": true, "CanDownloadRecovery": false}},
		{"gotowe", map[string]bool{"CanVisibility": false, "CanGrants": false, "CanAdd": false, "CanConnect": false, "CanLocate": false, "CanDetach": false, "CanDelete": false, "CanLoadDump": false, "CanDeactivate": false, "CanRemoveRealm": false, "CanDownloadRecovery": true}},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for i, expected := range want {
		if got := rows[i]["Folder"]; got != expected.folder {
			t.Fatalf("row %d folder = %v, want %q", i, got, expected.folder)
		}
		for capability, wantEnabled := range expected.caps {
			if got, ok := rows[i][capability].(bool); !ok || got != wantEnabled {
				t.Errorf("row %d (%s) %s = %v, want %v", i, expected.folder, capability, rows[i][capability], wantEnabled)
			}
		}
	}
}

// capturedScript returns the argument every dialog method passes to
// powershell.exe after -Command, i.e. the script the backend actually
// generated. Asserting on substrings of that script is not enough: a
// generated script can contain every expected fragment and still be
// syntactically invalid as a whole (see
// TestWindowsGeneratedScriptsAreValidPowerShell).
func capturedScript(t *testing.T, runner *fakeWindowsRunner) string {
	t.Helper()
	calls := runner.Calls()
	if len(calls) != 1 {
		t.Fatalf("powershell invocations = %d, want 1", len(calls))
	}
	args := calls[0].args
	for i, arg := range args {
		if arg == "-Command" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("no -Command argument in %q", args)
	return ""
}

// assertPowerShellParses feeds a generated script to the real Windows
// PowerShell parser. It deliberately does not execute it: parsing alone is
// what catches a malformed generator, and executing would open real windows.
//
// The script is handed over as a file rather than inline so that quoting in
// the script under test cannot leak into the quoting of the check itself --
// which is exactly the class of bug this test exists to catch.
func assertPowerShellParses(t *testing.T, label, script string) {
	t.Helper()
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("powershell.exe unavailable: %v", err)
	}
	path := filepath.Join(t.TempDir(), "script.ps1")
	// UTF-8 with BOM: the generated scripts carry Polish labels, and
	// Windows PowerShell 5.1 assumes the ANSI codepage for a BOM-less file.
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte(script)...), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	// The path travels in the environment, not in the command string:
	// powershell.exe -Command appends any trailing arguments to the command
	// itself rather than exposing them as $args.
	const check = `$e=$null;[void][System.Management.Automation.Language.Parser]::ParseFile($env:FILEES_TEST_SCRIPT,[ref]$null,[ref]$e);if($e.Count -gt 0){$e|ForEach-Object{$_.Message};exit 1}`
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", check)
	cmd.Env = append(os.Environ(), "FILEES_TEST_SCRIPT="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("%s: generated script is not valid PowerShell: %v\n%s", label, err, out)
	}
}

// TestWindowsGeneratedScriptsAreValidPowerShell covers every WinForms/toast
// script the Windows backend generates. Added after the settings dialog was
// found to emit a script that fails to parse outright -- the concatenation
// around the answer separator was written as "$a+':[string]$g...Cells['ID']"
// instead of "$a+':'+[string]$g...Cells['ID']", so the single quotes meant to
// delimit the separator swallowed the cell lookup. PowerShell parses the whole
// -Command payload before running any of it, so the window never opened at
// all; the user got an operational-failure toast instead of a dialog. No test
// existed for any generated script, which is why a syntax error shipped.
//
// Data deliberately includes Polish characters, an apostrophe (psString
// escaping) and a colon inside an operation ID.
func TestWindowsGeneratedScriptsAreValidPowerShell(t *testing.T) {
	settings := SettingsDialogRequest{
		Title: "Ustawienia FileES",
		Text:  "Serwery i foldery",
		Servers: []SettingsServer{{
			ID: "biuro", Name: "Biuro l'Atelier", Address: "svn://example", Realm: "strefa", ClientID: "c1",
			CanSetRealmVisibility: true, CanAddFolder: true,
			Folders: []SettingsFolder{
				{ID: "repo-1", Name: "Rysunki", LocalPath: `C:\wc\repo-1`, State: "aktywny", Access: "rw", CanManageGrants: true, CanManagePublicShares: true, CanDetach: true, CanDelete: true, CanLoadDump: true},
				{ID: "repo-2", Name: "Zdjęcia", LocalPath: `C:\wc\repo-2`, State: "wstrzymany", Access: "r"},
			},
		}, {ID: "pusty", Name: "Bez folderów", Address: "svn://empty", Realm: "strefa", ClientID: "c2"}},
		Recoveries: []SettingsRecovery{
			{OperationID: "op-1", ServerName: "Biuro", KitPath: `C:\kit.fkr`, Status: "gotowe", CanDownload: true},
			{OperationID: "op-2", ServerName: "Biuro", KitPath: `C:\kit2.fkr`, Status: "oczekuje"},
		},
	}
	cases := []struct {
		name string
		call func(*testing.T, *WindowsBackend)
	}{
		{"settings", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.ShowSettings(context.Background(), settings); err != nil {
				t.Fatalf("ShowSettings: %v", err)
			}
		}},
		{"realm_grants", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.ShowRealmGrants(context.Background(), RealmGrantDialogRequest{Title: "Dostęp stref", Text: "Wybierz", Recipients: []RealmGrantRecipient{{RealmID: "r1", Alias: "Zespół l'A"}}}); err != nil {
				t.Fatalf("ShowRealmGrants: %v", err)
			}
		}},
		{"public_shares", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.ShowPublicShares(context.Background(), PublicShareDialogRequest{Title: "Udostępnienia", Text: "Kanały", Shares: []PublicShareSummary{{ChannelID: "c1", Address: "acme/wydanie", State: "aktywne", SourceRoot: "public", Recipients: "kanał otwarty", Password: "brak", Revision: "HEAD"}}}); err != nil {
				t.Fatalf("ShowPublicShares: %v", err)
			}
		}},
		{"realm_visibility", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.ShowRealmVisibility(context.Background(), RealmVisibilityDialogRequest{Title: "Widoczność", Text: "Wybierz tryb"}); err != nil {
				t.Fatalf("ShowRealmVisibility: %v", err)
			}
		}},
		{"reservations", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.ShowReservations(context.Background(), ReservationDialogRequest{Title: "Rezerwacje", Text: "Aktywne", Rows: []ReservationDialogRow{{ID: "res-1", Server: "Biuro", WorkingCopy: "Rysunki", Path: `rysunki\a.dwg`, Owner: "acme", CreatedAt: "2026-08-03", Action: "zwolnij"}}}); err != nil {
				t.Fatalf("ShowReservations: %v", err)
			}
		}},
		{"journal", func(t *testing.T, b *WindowsBackend) {
			if err := b.ShowJournal(context.Background(), JournalDialogRequest{Title: "Dziennik FileES", Text: "Aktywność i błędy", Rows: []JournalDialogRow{{Timestamp: "2026-08-10 12:00:00", Repository: "Zdjęcia", Summary: "⚠ BŁĄD · odmowa", Details: "Wymagane działanie", Severity: "ERROR", Emphasized: true}}}); err != nil {
				t.Fatalf("ShowJournal: %v", err)
			}
		}},
		{"prompt", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.PromptText(context.Background(), PromptTextRequest{Title: "Kod", Text: "Podaj kod OTP z e-maila", Placeholder: "np. 123456"}); err != nil {
				t.Fatalf("PromptText: %v", err)
			}
		}},
		{"info", func(t *testing.T, b *WindowsBackend) {
			if err := b.ShowInfo(context.Background(), InfoRequest{Title: "Informacje", Text: "Powiązanie zakończone na serwerze"}); err != nil {
				t.Fatalf("ShowInfo: %v", err)
			}
		}},
		{"confirm", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.Confirm(context.Background(), ConfirmRequest{Title: "Potwierdź", Text: "Czy na pewno?", ConfirmText: "Tak", CancelText: "Nie"}); err != nil {
				t.Fatalf("Confirm: %v", err)
			}
		}},
		{"consent", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.ConfirmConsent(context.Background(), ConsentRequest{Title: "Zgoda", Text: "Usunięcie repozytorium", RequiredText: "Rozumiem, że dane zostaną usunięte", OptionalText: "Poproś też o usunięcie danych"}); err != nil {
				t.Fatalf("ConfirmConsent: %v", err)
			}
		}},
		{"file_picker", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.PickFiles(context.Background(), PickFilesRequest{Title: "Wybierz pliki", Root: `C:\wc\repo-1`, AllowMultiple: true}); err != nil {
				t.Fatalf("PickFiles: %v", err)
			}
		}},
		{"folder_picker", func(t *testing.T, b *WindowsBackend) {
			if _, err := b.PickFolder(context.Background(), PickFolderRequest{Title: "Wybierz folder", InitialDir: `C:\wc`}); err != nil {
				t.Fatalf("PickFolder: %v", err)
			}
		}},
		{"toast", func(t *testing.T, b *WindowsBackend) {
			if err := b.Notify(context.Background(), Notification{ID: "n1", Group: "repo-1", Title: "Zmiany pobrane", Body: "Zaktualizowano l'Atelier", Urgency: UrgencyNormal}); err != nil {
				t.Fatalf("Notify: %v", err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeWindowsRunner{}
			backend := newTestWindowsBackend(runner, time.Now)
			test.call(t, backend)
			script := capturedScript(t, runner)
			assertPowerShellParses(t, test.name, script)
			if test.name != "toast" {
				if !strings.Contains(script, "$f.TopMost=$true") && !strings.Contains(script, "$owner.TopMost=$true") {
					t.Fatal("modal script has no persistent TopMost window or owner")
				}
				if strings.Contains(script, "$f.TopMost=$false") || strings.Contains(script, "$owner.Hide()") {
					t.Fatal("modal script drops its foreground anchor before the dialog closes")
				}
			}
		})
	}
}

// Placeholder and Default were one field, and it quietly did the wrong thing
// for half its callers: the hint was written into the box as content, so
// "Kod OTP" or "filees-invite:v1:…" arrived as the answer when the user just
// pressed OK - and with Secret set it was masked, so they could not see what
// they were about to send.
func TestPromptHintIsNeverFieldContentButADefaultIs(t *testing.T) {
	hint := buildPromptScript(PromptTextRequest{Title: "t", Text: "paste it", Placeholder: "filees-invite:v1:…", Secret: true})
	if strings.Contains(hint, "$t.Text='filees-invite:v1:…'") {
		t.Fatalf("hint was written into the field as content:\n%s", hint)
	}
	if !strings.Contains(hint, "PlaceholderText") {
		t.Fatalf("hint was dropped entirely instead of shown as a placeholder:\n%s", hint)
	}
	// Probed rather than assigned outright: setting an absent property is a
	// terminating error in PowerShell, and losing a hint beats losing the
	// dialog.
	if !strings.Contains(hint, "PSObject.Properties['PlaceholderText']") {
		t.Fatalf("PlaceholderText was assumed present rather than probed:\n%s", hint)
	}

	value := buildPromptScript(PromptTextRequest{Title: "t", Text: "name", Default: "ZEGRZE"})
	if !strings.Contains(value, "$t.Text='ZEGRZE'") {
		t.Fatalf("default value was not prefilled:\n%s", value)
	}
}
