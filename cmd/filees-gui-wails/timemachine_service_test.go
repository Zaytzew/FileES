package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The page and the Go side must agree on the module and the event name; a
// mismatch leaves a window that opens and never learns which repository.
func TestTimeMachinePageUsesItsBindingAndContextEvent(t *testing.T) {
	page, err := frontend.ReadFile("frontend/timemachine.html")
	if err != nil || !strings.Contains(string(page), `src="./timemachine.js"`) {
		t.Fatalf("time machine page missing or not loading its script: %v", err)
	}
	script, err := frontend.ReadFile("frontend/timemachine.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"./bindings/filees/cmd/filees-gui-wails/timemachineservice.js", `"` + timeMachineContextEvent + `"`, "TimeMachine.Close()", "TimeMachine.Confirm(", "TimeMachine.Cancel("} {
		if !strings.Contains(string(script), wanted) {
			t.Fatalf("time machine script missing %q", wanted)
		}
	}
}

// The window's binding module is kept by hand; its IDs must be the ones the
// build context gives these methods, and nothing Go-only may be exposed.
func TestTimeMachineFrontendBindingUsesBuildContextIDs(t *testing.T) {
	_ = application.New(application.Options{})
	bindings := application.NewBindings(nil, nil)
	service, _ := newTestTimeMachine(timeMachineSnapshot())
	if err := bindings.Add(application.NewService(service)); err != nil {
		t.Fatal(err)
	}
	module, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/timemachineservice.js")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{
		"Repositories": "3993252539", "Context": "3963988158", "Resolve": "3005509185", "Commits": "2437090663",
		"Changes": "285647072", "List": "1949481919", "Density": "3998683337", "ChooseDestination": "2002927558",
		"Fetch": "1963484241", "Operation": "1519860772", "Confirm": "3305799153", "Cancel": "3346073713", "Close": "1754646125",
	}
	for method, id := range ids {
		name := "filees/cmd/filees-gui-wails.TimeMachineService." + method
		bound := bindings.Get(&application.CallOptions{MethodName: name})
		if bound == nil {
			t.Fatalf("binding not found: %s", name)
		}
		if !strings.Contains(string(module), "export function "+method+"(") || !strings.Contains(string(module), "ByID("+id) {
			t.Fatalf("frontend module lacks %s with build-context ID %s", method, id)
		}
	}
	for _, internal := range []string{"open", "offers", "failure", "attachPicker"} {
		if bindings.Get(&application.CallOptions{MethodName: "filees/cmd/filees-gui-wails.TimeMachineService." + internal}) != nil {
			t.Fatalf("Go-only method %s was exposed to the WebView", internal)
		}
	}
}

type fakeTimeMachineDaemon struct {
	resolved []contract.RepoHistoryResolvePayload
	fetched  []contract.RepoHistoryFetchPayload
	err      error
}

func (f *fakeTimeMachineDaemon) HistoryResolve(_ context.Context, payload contract.RepoHistoryResolvePayload) (*contract.RepoHistorySnapshot, error) {
	f.resolved = append(f.resolved, payload)
	if f.err != nil {
		return nil, f.err
	}
	return &contract.RepoHistorySnapshot{SnapshotID: "snap-1", RepoID: payload.RepoID, Revision: 25}, nil
}

func (f *fakeTimeMachineDaemon) HistoryCommits(context.Context, contract.RepoHistoryCommitsPayload) (*contract.RepoHistoryCommitsResult, error) {
	return &contract.RepoHistoryCommitsResult{}, f.err
}

func (f *fakeTimeMachineDaemon) HistoryChanges(context.Context, contract.RepoHistoryChangesPayload) (*contract.RepoHistoryChangesResult, error) {
	return &contract.RepoHistoryChangesResult{}, f.err
}

func (f *fakeTimeMachineDaemon) HistoryList(context.Context, contract.RepoHistoryListPayload) (*contract.RepoHistoryListResult, error) {
	return &contract.RepoHistoryListResult{}, f.err
}

func (f *fakeTimeMachineDaemon) HistoryDensity(context.Context, contract.RepoHistoryDensityPayload) (*contract.RepoHistoryDensityResult, error) {
	return &contract.RepoHistoryDensityResult{}, f.err
}

func (f *fakeTimeMachineDaemon) HistoryFetch(_ context.Context, payload contract.RepoHistoryFetchPayload) (*contract.RepoHistoryOperation, error) {
	f.fetched = append(f.fetched, payload)
	return &contract.RepoHistoryOperation{OperationID: "op-1", State: "planning"}, f.err
}

func (f *fakeTimeMachineDaemon) HistoryOperation(context.Context, string) (*contract.RepoHistoryOperation, error) {
	return &contract.RepoHistoryOperation{}, f.err
}

func (f *fakeTimeMachineDaemon) HistoryConfirm(context.Context, string) (*contract.RepoHistoryOperation, error) {
	return &contract.RepoHistoryOperation{}, f.err
}

func (f *fakeTimeMachineDaemon) HistoryCancel(context.Context, string) (*contract.RepoHistoryOperation, error) {
	return &contract.RepoHistoryOperation{}, f.err
}

type historyEventRecorder struct {
	names []string
	data  []any
}

func (r *historyEventRecorder) Emit(name string, data ...any) bool {
	r.names = append(r.names, name)
	r.data = append(r.data, data...)
	return true
}

func timeMachineSnapshot() Snapshot {
	return Snapshot{
		Connected: true, Capabilities: []string{contract.CapRepoHistory},
		Servers: []ServerProjection{{ID: "office", DisplayName: "Biuro"}, {ID: "home", DisplayName: "Dom"}},
		Repositories: []RepoProjection{
			{ID: "zeta", ServerID: "office", DisplayName: "Zeta", Ownership: "owned", LocalPath: "E:/Zeta"},
			{ID: "proj", ServerID: "office", DisplayName: "Projekt", Ownership: "owned"},
			{ID: "archive", ServerID: "home", DisplayName: "Archiwum", Ownership: "owned"},
			{ID: "guest", ServerID: "office", DisplayName: "Cudze", Ownership: "guest"},
			{ID: "shelf", ServerID: "office", DisplayName: "Półka", Ownership: "owned", Purpose: "upload_shelf"},
			{ID: "gone", ServerID: "office", DisplayName: "Usunięte", Ownership: "owned", ServerDeleted: true},
		},
	}
}

func newTestTimeMachine(snapshot Snapshot) (*TimeMachineService, *fakeTimeMachineDaemon) {
	daemon := &fakeTimeMachineDaemon{}
	service := newTimeMachineService(daemon, func() Snapshot { return snapshot },
		func(code, key string, _ map[string]string) string { return code + " " + key },
		func(_ string, fallback string) string { return fallback })
	return service, daemon
}

func TestTimeMachineOffersOnlyOwnedOrdinaryRepositories(t *testing.T) {
	service, _ := newTestTimeMachine(timeMachineSnapshot())
	var got []string
	for _, repo := range service.Repositories() {
		got = append(got, repo.ServerName+"/"+repo.Name)
	}
	if !reflect.DeepEqual(got, []string{"Biuro/Projekt", "Biuro/Zeta", "Dom/Archiwum"}) {
		t.Fatalf("offered = %v", got)
	}

	for name, snapshot := range map[string]Snapshot{
		"no capability": func() Snapshot { s := timeMachineSnapshot(); s.Capabilities = nil; return s }(),
		"stale":         func() Snapshot { s := timeMachineSnapshot(); s.Stale = true; return s }(),
		"disconnected":  func() Snapshot { s := timeMachineSnapshot(); s.Connected = false; return s }(),
	} {
		service, _ := newTestTimeMachine(snapshot)
		if repos := service.Repositories(); len(repos) != 0 || repos == nil {
			t.Errorf("%s: offered %v", name, repos)
		}
	}
}

func TestTimeMachineResolveAsksTheDaemonOnlyForOfferedRepositories(t *testing.T) {
	service, daemon := newTestTimeMachine(timeMachineSnapshot())
	for _, repoID := range []string{"guest", "shelf", "gone", "unknown"} {
		if _, err := service.Resolve("office", repoID, "", ""); err == nil || err.Error() != "HISTORY-2001 history.forbidden" {
			t.Fatalf("%s: %v", repoID, err)
		}
	}
	if _, err := service.Resolve("home", "proj", "", ""); err == nil {
		t.Fatal("a repository offered on another server was accepted")
	}
	if len(daemon.resolved) != 0 {
		t.Fatalf("daemon asked for refused repositories: %v", daemon.resolved)
	}
	snap, err := service.Resolve("office", "proj", "2026-09-12T10:00:00Z", "at")
	if err != nil || snap.Revision != 25 {
		t.Fatalf("snapshot = %+v %v", snap, err)
	}
	if want := (contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "proj", MomentUTC: "2026-09-12T10:00:00Z", Boundary: "at"}); daemon.resolved[0] != want {
		t.Fatalf("payload = %+v", daemon.resolved[0])
	}
}

func TestTimeMachineRendersDaemonRefusalsFromTheCatalogue(t *testing.T) {
	service, daemon := newTestTimeMachine(timeMachineSnapshot())
	daemon.err = &ipcclient.ResponseError{Body: contract.ErrorBody{Code: "HISTORY-2002", MessageKey: "history.snapshot_unknown"}}
	if _, err := service.List("snap-1", "", ""); err == nil || err.Error() != "HISTORY-2002 history.snapshot_unknown" {
		t.Fatalf("refusal = %v", err)
	}
	daemon.err = errors.New("dial unix: connection refused")
	if _, err := service.Commits("snap-1", "", "", ""); err == nil || err.Error() != "HISTORY-1001 history.read_failed" {
		t.Fatalf("transport failure = %v", err)
	}
}

func TestTimeMachineFetchRefusesARelativeDestinationLocally(t *testing.T) {
	service, daemon := newTestTimeMachine(timeMachineSnapshot())
	if _, err := service.Fetch(contract.RepoHistoryFetchPayload{SnapshotID: "snap-1", Selection: contract.HistorySelectionAll, DestinationParent: "Pobrane"}); err == nil || err.Error() != "HISTORY-2005 history.destination_refused" {
		t.Fatalf("relative destination: %v", err)
	}
	if len(daemon.fetched) != 0 {
		t.Fatal("daemon asked to export into a relative path")
	}
	parent := t.TempDir()
	op, err := service.Fetch(contract.RepoHistoryFetchPayload{SnapshotID: "snap-1", Selection: contract.HistorySelectionAll, DestinationParent: parent})
	if err != nil || op.OperationID != "op-1" || daemon.fetched[0].DestinationParent != parent {
		t.Fatalf("operation = %+v %v", op, err)
	}
}

func TestTimeMachineOpenFocusesTheWindowOnOfferedRepositories(t *testing.T) {
	service, _ := newTestTimeMachine(timeMachineSnapshot())
	emitter := &historyEventRecorder{}
	shown := 0
	service.attachEmitter(emitter)
	service.attachPresentation(func() { shown++ }, func() {})
	adapter := timeMachineBrowserAdapter{service: service}

	if err := adapter.OpenHistory(t.Context(), platform.HistoryOpenRequest{ServerID: "office", RepoID: "guest"}); err == nil || shown != 0 {
		t.Fatalf("guest repository opened: %v shown=%d", err, shown)
	}
	for _, request := range []platform.HistoryOpenRequest{{ServerID: "office", RepoID: "proj"}, {ServerID: "office", RepoID: "proj"}, {}} {
		if err := adapter.OpenHistory(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	if shown != 3 || len(emitter.names) != 3 || emitter.names[0] != timeMachineContextEvent {
		t.Fatalf("shown=%d events=%v", shown, emitter.names)
	}
	second := emitter.data[1].(TimeMachineContext)
	if second.RepoID != "proj" || second.Sequence != 2 {
		t.Fatalf("reopening the same repository = %+v", second)
	}
	if focus := service.Context(); focus.RepoID != "" || focus.Sequence != 3 {
		t.Fatalf("tray opening = %+v", focus)
	}
}

func TestTimeMachineChooseDestinationUsesTheNativePicker(t *testing.T) {
	service, _ := newTestTimeMachine(timeMachineSnapshot())
	if _, err := service.ChooseDestination(); err == nil {
		t.Fatal("chose a destination without a picker")
	}
	chosen := filepath.Join(t.TempDir(), "kopie")
	var title string
	service.attachPicker(func(_ context.Context, gotTitle, initial string) (string, error) {
		title = gotTitle
		if !filepath.IsAbs(initial) {
			t.Errorf("initial directory %q", initial)
		}
		return chosen, nil
	})
	if got, err := service.ChooseDestination(); err != nil || got != chosen || title == "" {
		t.Fatalf("destination = %q %v title=%q", got, err, title)
	}
}
