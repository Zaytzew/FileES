//go:build windows

package platform

import (
	"context"
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

func TestPowerShellOutputForcesUTF8WithoutMutatingCallerArgs(t *testing.T) {
	original := []string{"-NoProfile", "-Command", "[Console]::Write('ŁÓDŹ')"}
	got := powerShellUTF8OutputArgs(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, original)
	if !strings.HasPrefix(got[2], powerShellUTF8OutputPrelude) || !strings.HasSuffix(got[2], original[2]) {
		t.Fatalf("prepared command = %q", got[2])
	}
	if got[2] == original[2] || original[2] != "[Console]::Write('ŁÓDŹ')" {
		t.Fatalf("caller args were mutated: original=%q got=%q", original[2], got[2])
	}
	other := powerShellUTF8OutputArgs(`C:\Windows\System32\cmd.exe`, original)
	if !reflect.DeepEqual(other, original) {
		t.Fatalf("non-PowerShell command changed: %#v", other)
	}
}

func TestPowerShellOutputReturnsLiveUnicodeAsUTF8(t *testing.T) {
	command, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("Windows PowerShell unavailable: %v", err)
	}
	out, err := (osWindowsCommandRunner{}).Output(context.Background(), command, "-NoProfile", "-NonInteractive", "-Command", "[Console]::Write('ŁÓDŹ-kolumny')")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "ŁÓDŹ-kolumny"; got != want {
		t.Fatalf("PowerShell output = %q, want valid UTF-8 %q", got, want)
	}
}

func TestPowerShellOutputRejectsBytesOutsideUTF8(t *testing.T) {
	command, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("Windows PowerShell unavailable: %v", err)
	}
	_, err = (osWindowsCommandRunner{}).Output(context.Background(), command, "-NoProfile", "-NonInteractive", "-Command", "$o=[Console]::OpenStandardOutput();$o.Write([byte[]](0xA3),0,1)")
	if err == nil || !strings.Contains(err.Error(), "outside UTF-8") {
		t.Fatalf("invalid PowerShell output error = %v", err)
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

// Execute the actual production prefix in a fresh Windows PowerShell process.
// No Show call: this checks WinRT activation without sending a user notification.
func TestWindowsToastObjectsConstructInFreshPowerShell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script := buildToastScript(Notification{Title: "Windows E2E O'Brien", Body: "Zażółć <&>"}, "test-tag", "ATMProjekt.FileES")
	end := strings.LastIndex(script, "[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier")
	if end < 0 {
		t.Fatal("missing Show boundary")
	}
	script = script[:end] + "if($toast.Tag -ne 'test-tag'){throw 'invalid tag'};Write-Output 'constructed'"
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "constructed") {
		t.Fatalf("toast construction: %v: %s", err, out)
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
