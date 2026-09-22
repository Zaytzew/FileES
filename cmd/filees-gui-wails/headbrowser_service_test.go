package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	contract "filees/pkg/contract/v1"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeHeadBrowserDaemon struct {
	lists        []contract.RepoHeadListPayload
	cats         []contract.RepoHeadCatPayload
	materialized []contract.RepoHeadMaterializePayload
	fills        []contract.RepoHeadFillPayload
}

func (f *fakeHeadBrowserDaemon) HeadList(_ context.Context, p contract.RepoHeadListPayload) (*contract.RepoHeadListResult, error) {
	f.lists = append(f.lists, p)
	return &contract.RepoHeadListResult{Path: p.Path, Entries: []contract.RepoHeadEntry{{Name: "art", Kind: "dir"}}}, nil
}

func (f *fakeHeadBrowserDaemon) HeadCat(_ context.Context, p contract.RepoHeadCatPayload) (*contract.RepoHeadCatResult, error) {
	f.cats = append(f.cats, p)
	return &contract.RepoHeadCatResult{Path: p.Path, File: filepath.Join(filepath.FromSlash("/tmp"), "preview.psd")}, nil
}

func (f *fakeHeadBrowserDaemon) HeadMaterialize(_ context.Context, p contract.RepoHeadMaterializePayload) (*contract.RepoHeadWriteResult, error) {
	f.materialized = append(f.materialized, p)
	return &contract.RepoHeadWriteResult{LocalPath: p.LocalPath, Path: p.Path, State: "attaching", OperationID: "op"}, nil
}

func (f *fakeHeadBrowserDaemon) HeadFill(_ context.Context, p contract.RepoHeadFillPayload) (*contract.RepoHeadWriteResult, error) {
	f.fills = append(f.fills, p)
	return &contract.RepoHeadWriteResult{State: "attached"}, nil
}

func (f *fakeHeadBrowserDaemon) RepoLifecycleStatus(_ context.Context, operationID string) (*contract.RepoLifecycleResult, error) {
	return &contract.RepoLifecycleResult{OperationID: operationID, State: "attached"}, nil
}

func headBrowserSnapshot() Snapshot {
	return Snapshot{
		Connected: true, Capabilities: []string{contract.CapRepoHeadBrowse},
		Servers: []ServerProjection{{ID: "office", DisplayName: "Biuro"}},
		Repositories: []RepoProjection{
			{ID: "remote", ServerID: "office", DisplayName: "Projekt: Atlas", Access: "rw"},
			{ID: "sparse", ServerID: "office", DisplayName: "Studio", Access: "r", Attached: true, Sparse: true, LocalPath: filepath.Join(filepath.FromSlash("/data"), "Studio")},
			{ID: "shelf", ServerID: "office", DisplayName: "Półka", Access: "r", Purpose: "upload_shelf"},
			{ID: "gone", ServerID: "office", DisplayName: "Usunięte", Access: "rw", ServerDeleted: true},
			{ID: "hidden", ServerID: "office", DisplayName: "Bez dostępu"},
		},
	}
}

func newTestHeadBrowser(snapshot Snapshot) (*HeadBrowserService, *fakeHeadBrowserDaemon) {
	daemon := &fakeHeadBrowserDaemon{}
	service := newHeadBrowserService(daemon, func() Snapshot { return snapshot },
		func(code, key string, _ map[string]string) string { return code + " " + key },
		func(_ string, fallback string) string { return fallback })
	return service, daemon
}

func TestHeadBrowserFrontendBindingUsesBuildContextIDs(t *testing.T) {
	_ = application.New(application.Options{})
	bindings := application.NewBindings(nil, nil)
	service, _ := newTestHeadBrowser(headBrowserSnapshot())
	if err := bindings.Add(application.NewService(service)); err != nil {
		t.Fatal(err)
	}
	module, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/headbrowserservice.js")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{
		"Open": "1302558521", "Context": "1296624396", "Repository": "635738405", "List": "1147532241",
		"Preview": "3881288903", "OpenLocal": "1320231244", "Materialize": "2128678882", "Fill": "2384190360",
		"Operation": "667235286", "Close": "2252102579",
	}
	for method, id := range ids {
		if bindings.Get(&application.CallOptions{MethodName: "filees/cmd/filees-gui-wails.HeadBrowserService." + method}) == nil {
			t.Fatalf("binding not found: %s", method)
		}
		if !strings.Contains(string(module), "export function "+method+"(") || !strings.Contains(string(module), "ByID("+id) {
			t.Fatalf("frontend module lacks %s with build-context ID %s", method, id)
		}
	}
	for _, internal := range []string{"lookup", "focused", "chooseAnchor", "openPath", "attachPlatform"} {
		if bindings.Get(&application.CallOptions{MethodName: "filees/cmd/filees-gui-wails.HeadBrowserService." + internal}) != nil {
			t.Fatalf("Go-only method %s was exposed to the WebView", internal)
		}
	}
}

// The window opens only on ordinary, readable, live repositories, and only
// against a daemon that serves the head commands.
func TestHeadBrowserOpensOnlyOnBrowsableRepositories(t *testing.T) {
	service, _ := newTestHeadBrowser(headBrowserSnapshot())
	for _, repo := range []string{"remote", "sparse"} {
		if err := service.Open("office", repo); err != nil {
			t.Fatalf("%s refused: %v", repo, err)
		}
	}
	for _, repo := range []string{"shelf", "gone", "hidden", "missing"} {
		if err := service.Open("office", repo); err == nil {
			t.Fatalf("%s was offered", repo)
		}
	}
	snapshot := headBrowserSnapshot()
	snapshot.Capabilities = nil
	older, _ := newTestHeadBrowser(snapshot)
	if err := older.Open("office", "remote"); err == nil {
		t.Fatal("offered against a daemon without the capability")
	}
}

// The first path asks for a parent folder and names the copy after the
// repository; a cancelled dialog asks the daemon for nothing.
func TestFirstPathAsksWhereTheCopyGoes(t *testing.T) {
	service, daemon := newTestHeadBrowser(headBrowserSnapshot())
	parent := filepath.Join(filepath.FromSlash("/home/user"), "Projekty")
	answer := ""
	service.attachPlatform(func(context.Context, string, string) (string, error) { return answer, nil }, nil)
	if err := service.Open("office", "remote"); err != nil {
		t.Fatal(err)
	}
	result, err := service.Materialize("art/model.blend")
	if err != nil || result.State != "cancelled" || len(daemon.materialized) != 0 {
		t.Fatalf("cancelled dialog: result=%+v err=%v calls=%d", result, err, len(daemon.materialized))
	}
	answer = parent
	if _, err := service.Materialize("art/model.blend"); err != nil {
		t.Fatal(err)
	}
	got := daemon.materialized[0]
	if got.LocalPath != filepath.Join(parent, "Projekt_ Atlas") || got.Path != "art/model.blend" {
		t.Fatalf("materialize payload = %+v", got)
	}
}

// Later paths join the existing copy without a dialog; the whole folder is
// offered only when a copy exists.
func TestLaterPathsAndFillNeedNoDialog(t *testing.T) {
	service, daemon := newTestHeadBrowser(headBrowserSnapshot())
	service.attachPlatform(func(context.Context, string, string) (string, error) {
		t.Fatal("picker shown for a repository that already has a copy")
		return "", nil
	}, nil)
	if err := service.Open("office", "sparse"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Materialize("docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Fill(); err != nil {
		t.Fatal(err)
	}
	if len(daemon.materialized) != 1 || daemon.materialized[0].LocalPath != "" || len(daemon.fills) != 1 {
		t.Fatalf("materialized=%+v fills=%d", daemon.materialized, len(daemon.fills))
	}

	remote, _ := newTestHeadBrowser(headBrowserSnapshot())
	if err := remote.Open("office", "remote"); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Fill(); err == nil {
		t.Fatal("fill offered without a copy")
	}
}

func TestHeadBrowserOpenLocalStaysInsideTheCopy(t *testing.T) {
	snapshot := headBrowserSnapshot()
	copyRoot := t.TempDir()
	snapshot.Repositories[1].LocalPath = copyRoot
	service, _ := newTestHeadBrowser(snapshot)
	var opened []string
	service.attachPlatform(nil, func(_ context.Context, path string) error {
		opened = append(opened, path)
		return nil
	})
	if err := service.Open("office", "sparse"); err != nil {
		t.Fatal(err)
	}
	if err := service.OpenLocal("art/model.blend"); err != nil {
		t.Fatal(err)
	}
	if err := service.OpenLocal("../../etc"); err == nil {
		t.Fatal("path outside the copy was opened")
	}
	want := filepath.Join(copyRoot, "art", "model.blend")
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened = %v, want %s", opened, want)
	}
}
