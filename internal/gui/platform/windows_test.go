//go:build windows

package platform

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type windowsCommandCall struct {
	name string
	args []string
	run  bool // true=Run, false=Output
}

type fakeWindowsRunner struct {
	mu     sync.Mutex
	calls  []windowsCommandCall
	output func(context.Context, string, []string) ([]byte, error)
	run    func(context.Context, string, []string) error
}

func (f *fakeWindowsRunner) Run(ctx context.Context, name string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, windowsCommandCall{name: name, args: append([]string(nil), args...), run: true})
	fn := f.run
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, name, args)
	}
	return nil
}

func (f *fakeWindowsRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, windowsCommandCall{name: name, args: append([]string(nil), args...), run: false})
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
	return newWindowsBackend(runner, now)
}

func TestWindowsOpenFolderCallsExplorer(t *testing.T) {
	runner := &fakeWindowsRunner{}
	backend := newTestWindowsBackend(runner, time.Now)
	if err := backend.OpenFolder(context.Background(), `C:\projects\work`); err != nil {
		t.Fatal(err)
	}
	calls := runner.Calls()
	if len(calls) != 1 || calls[0].name != "explorer.exe" ||
		len(calls[0].args) != 1 || calls[0].args[0] != `C:\projects\work` {
		t.Fatalf("calls = %#v", calls)
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
	if len(calls) != 1 || calls[0].name != "powershell.exe" {
		t.Fatalf("calls = %#v", calls)
	}
	script := calls[0].args[len(calls[0].args)-1]
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
	for _, expected := range []string{"ToastText02", "repo-sync", "Synchronizacja", "Pliki zsynchronizowane"} {
		if !contains(capturedScript, expected) {
			t.Errorf("toast script missing %q: %s", expected, capturedScript)
		}
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
	backend := NewWindowsBackend()

	// Remove any leftover entry from a prior run.
	_ = backend.SetAutostart(ctx, spec, false)

	state, err := backend.AutostartStatus(ctx, spec)
	if err != nil || state.Enabled {
		t.Fatalf("initial status = %#v, %v", state, err)
	}

	if err := backend.SetAutostart(ctx, spec, true); err != nil {
		t.Fatalf("enable autostart: %v", err)
	}
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || !state.Enabled {
		t.Fatalf("enabled status = %#v, %v", state, err)
	}

	if err := backend.SetAutostart(ctx, spec, false); err != nil {
		t.Fatalf("disable autostart: %v", err)
	}
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || state.Enabled {
		t.Fatalf("disabled status = %#v, %v", state, err)
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
