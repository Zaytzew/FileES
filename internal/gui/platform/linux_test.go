//go:build linux

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type linuxCommandCall struct {
	name string
	args []string
}

type fakeLinuxRunner struct {
	mu     sync.Mutex
	paths  map[string]string
	calls  []linuxCommandCall
	output func(context.Context, string, []string) ([]byte, error)
}

func (f *fakeLinuxRunner) LookPath(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path := f.paths[name]; path != "" {
		return path, nil
	}
	return "", os.ErrNotExist
}

func (f *fakeLinuxRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, linuxCommandCall{name: name, args: append([]string(nil), args...)})
	fn := f.output
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, name, args)
	}
	return nil, nil
}

func (f *fakeLinuxRunner) Calls() []linuxCommandCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]linuxCommandCall(nil), f.calls...)
}

type fakeExitError int

func (e fakeExitError) Error() string { return "exit" }
func (e fakeExitError) ExitCode() int { return int(e) }

func newTestLinuxBackend(runner *fakeLinuxRunner, configHome string, now func() time.Time) *LinuxBackend {
	return newLinuxBackend(runner, func() (string, error) { return configHome, nil }, now)
}

func TestLinuxOpenFolderUsesXDGOpen(t *testing.T) {
	runner := &fakeLinuxRunner{paths: map[string]string{"xdg-open": "/usr/bin/xdg-open"}}
	backend := newTestLinuxBackend(runner, t.TempDir(), time.Now)
	if err := backend.OpenFolder(context.Background(), "/wc/project"); err != nil {
		t.Fatal(err)
	}
	calls := runner.Calls()
	if len(calls) != 1 || calls[0].name != "/usr/bin/xdg-open" ||
		len(calls[0].args) != 1 || calls[0].args[0] != "/wc/project" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestLinuxOpenFolderReportsUnavailableAndRejectsRelativePath(t *testing.T) {
	backend := newTestLinuxBackend(&fakeLinuxRunner{}, t.TempDir(), time.Now)
	if err := backend.OpenFolder(context.Background(), "/wc"); !IsFailure(err, FailureUnavailable) {
		t.Fatalf("missing xdg-open error = %v", err)
	}
	if err := backend.OpenFolder(context.Background(), "relative"); !IsFailure(err, FailureOperational) {
		t.Fatalf("relative path error = %v", err)
	}
}

func TestLinuxPickerUsesZenityAndValidatesSelection(t *testing.T) {
	runner := &fakeLinuxRunner{
		paths: map[string]string{"zenity": "/usr/bin/zenity", "kdialog": "/usr/bin/kdialog"},
		output: func(context.Context, string, []string) ([]byte, error) {
			return []byte("/wc/a.dwg\n/wc/sub/b.dwg\n"), nil
		},
	}
	backend := newTestLinuxBackend(runner, t.TempDir(), time.Now)
	result, err := backend.PickFiles(context.Background(), PickFilesRequest{
		Title: "Zablokuj", Root: "/wc", InitialDir: "/wc/sub", AllowMultiple: true,
	})
	if err != nil || result.Cancelled || len(result.Paths) != 2 {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}
	calls := runner.Calls()
	if len(calls) != 1 || calls[0].name != "/usr/bin/zenity" {
		t.Fatalf("calls = %#v", calls)
	}
	args := strings.Join(calls[0].args, "\n")
	for _, expected := range []string{"--file-selection", "--multiple", "--separator=\n", "--filename=/wc/sub/", "--title=Zablokuj"} {
		if !strings.Contains(args, expected) {
			t.Errorf("zenity args missing %q: %#v", expected, calls[0].args)
		}
	}
}

func TestLinuxPickerFallsBackToKDialog(t *testing.T) {
	runner := &fakeLinuxRunner{
		paths:  map[string]string{"kdialog": "/usr/bin/kdialog"},
		output: func(context.Context, string, []string) ([]byte, error) { return []byte("/wc/a.dwg\n"), nil },
	}
	backend := newTestLinuxBackend(runner, t.TempDir(), time.Now)
	result, err := backend.PickFiles(context.Background(), PickFilesRequest{Root: "/wc", AllowMultiple: true})
	if err != nil || len(result.Paths) != 1 {
		t.Fatalf("PickFiles() = %#v, %v", result, err)
	}
	if calls := runner.Calls(); len(calls) != 1 || calls[0].name != "/usr/bin/kdialog" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestLinuxPickerCancellationUnavailableAndOutsideRoot(t *testing.T) {
	runner := &fakeLinuxRunner{
		paths:  map[string]string{"zenity": "/usr/bin/zenity"},
		output: func(context.Context, string, []string) ([]byte, error) { return nil, fakeExitError(1) },
	}
	backend := newTestLinuxBackend(runner, t.TempDir(), time.Now)
	result, err := backend.PickFiles(context.Background(), PickFilesRequest{Root: "/wc"})
	if err != nil || !result.Cancelled {
		t.Fatalf("cancelled PickFiles() = %#v, %v", result, err)
	}

	runner.output = func(context.Context, string, []string) ([]byte, error) {
		return []byte("/other/secret.dwg\n"), nil
	}
	if _, err := backend.PickFiles(context.Background(), PickFilesRequest{Root: "/wc"}); !IsFailure(err, FailureOperational) {
		t.Fatalf("outside-root error = %v", err)
	}

	missing := newTestLinuxBackend(&fakeLinuxRunner{}, t.TempDir(), time.Now)
	if _, err := missing.PickFiles(context.Background(), PickFilesRequest{Root: "/wc"}); !IsFailure(err, FailureUnavailable) {
		t.Fatalf("missing picker error = %v", err)
	}
}

func TestLinuxNotificationsRateLimitAndReplaceByGroup(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	runner := &fakeLinuxRunner{
		paths: map[string]string{"notify-send": "/usr/bin/notify-send"},
		output: func(context.Context, string, []string) ([]byte, error) {
			return []byte("42\n"), nil
		},
	}
	backend := newTestLinuxBackend(runner, t.TempDir(), func() time.Time { return now })
	notification := Notification{ID: "offline", Group: "repo-a", Title: "Offline", Body: "Brak sieci", Urgency: UrgencyCritical}
	if err := backend.Notify(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	if err := backend.Notify(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.Calls()); got != 1 {
		t.Fatalf("rate-limited calls = %d, want 1", got)
	}

	now = now.Add(defaultNotificationInterval)
	if err := backend.Notify(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	calls := runner.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	args := strings.Join(calls[1].args, " ")
	if !strings.Contains(args, "--replace-id=42") || !strings.Contains(args, "--urgency=critical") {
		t.Fatalf("replacement args = %#v", calls[1].args)
	}
}

func TestLinuxNotificationFailureReleasesRateLimit(t *testing.T) {
	runner := &fakeLinuxRunner{
		paths:  map[string]string{"notify-send": "/usr/bin/notify-send"},
		output: func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("no session bus") },
	}
	backend := newTestLinuxBackend(runner, t.TempDir(), time.Now)
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

func TestLinuxAutostartLifecycleAndEscaping(t *testing.T) {
	configHome := t.TempDir()
	backend := newTestLinuxBackend(&fakeLinuxRunner{}, configHome, time.Now)
	spec := AutostartSpec{
		ID: "pl.filees.gui", Name: "FileES Tray",
		Executable: "/opt/FileES App/filees-gui",
		Args:       []string{"--profile", "50% \"ready\" $HOME"},
	}
	ctx := context.Background()

	state, err := backend.AutostartStatus(ctx, spec)
	if err != nil || state.Enabled {
		t.Fatalf("initial status = %#v, %v", state, err)
	}
	if err := backend.SetAutostart(ctx, spec, true); err != nil {
		t.Fatal(err)
	}
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || !state.Enabled || !state.Current {
		t.Fatalf("enabled status = %#v, %v", state, err)
	}
	data, err := os.ReadFile(state.Source)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		"Type=Application",
		"Name=FileES Tray",
		`Exec="/opt/FileES App/filees-gui" "--profile" "50%% \"ready\" \$HOME"`,
		"TryExec=/opt/FileES App/filees-gui",
		"Terminal=false",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("desktop entry missing %q:\n%s", expected, text)
		}
	}
	info, err := os.Stat(state.Source)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("desktop entry mode = %v", info.Mode().Perm())
	}
	if err := os.WriteFile(state.Source, []byte("[Desktop Entry]\nType=Application\nExec=/old/filees-gui\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || !state.Enabled || state.Current {
		t.Fatalf("stale status = %#v, %v", state, err)
	}
	if err := backend.SetAutostart(ctx, spec, true); err != nil {
		t.Fatal(err)
	}

	if err := backend.SetAutostart(ctx, spec, false); err != nil {
		t.Fatal(err)
	}
	state, err = backend.AutostartStatus(ctx, spec)
	if err != nil || state.Enabled {
		t.Fatalf("disabled status = %#v, %v", state, err)
	}
	disabled, err := os.ReadFile(state.Source)
	if err != nil || !strings.Contains(string(disabled), "Hidden=true") {
		t.Fatalf("disabled entry = %q, %v", disabled, err)
	}
}

func TestLinuxAutostartRejectsUnsafeInputAndCancellation(t *testing.T) {
	backend := newTestLinuxBackend(&fakeLinuxRunner{}, t.TempDir(), time.Now)
	if err := backend.SetAutostart(context.Background(), AutostartSpec{ID: "../escape"}, true); !IsFailure(err, FailureOperational) {
		t.Fatalf("unsafe ID error = %v", err)
	}
	if err := backend.SetAutostart(context.Background(), AutostartSpec{ID: "filees", Name: "FileES", Executable: "relative"}, true); !IsFailure(err, FailureOperational) {
		t.Fatalf("relative executable error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.SetAutostart(ctx, AutostartSpec{ID: "filees"}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled SetAutostart() = %v", err)
	}
	if _, err := backend.AutostartStatus(ctx, AutostartSpec{ID: "filees"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled AutostartStatus() = %v", err)
	}
}

func TestLinuxConcurrentNotificationsSameGroupSendOnce(t *testing.T) {
	runner := &fakeLinuxRunner{
		paths:  map[string]string{"notify-send": "/usr/bin/notify-send"},
		output: func(context.Context, string, []string) ([]byte, error) { return []byte("7"), nil },
	}
	backend := newTestLinuxBackend(runner, t.TempDir(), func() time.Time {
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

func TestRequirePathInsideRootDoesNotAcceptPrefixSibling(t *testing.T) {
	if err := requirePathInsideRoot("/wc-other/file", "/wc"); err == nil {
		t.Fatal("prefix sibling accepted as inside root")
	}
	if err := requirePathInsideRoot(filepath.Join("/wc", "sub", "..", "file"), "/wc"); err != nil {
		t.Fatalf("valid clean path rejected: %v", err)
	}
}
