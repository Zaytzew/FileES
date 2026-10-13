package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func TestLocalCopyPresentationSurvivesColdStartWithoutServerOrRuntime(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "local.json")
	store, err := localrepo.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginAttach("office", "repo-1", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(record.OperationID, "office", record.RepoID, "svn+ssh://example/repo-1", "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAttached(record.OperationID, record.RepoID); err != nil {
		t.Fatal(err)
	}
	store, err = localrepo.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stats := 0
	active := true
	view := &localCopyPresentation{store: store, active: func(string) bool { return active }, now: func() time.Time { return now }, stat: func(path string) (os.FileInfo, error) { stats++; return os.Lstat(path) }}
	rows := view.LocalRepositories()
	if len(rows) != 1 || rows[0].LocalCopy.Presence != "present" || rows[0].LocalPath != root || rows[0].Attached || rows[0].Access != "" {
		t.Fatalf("local fact fabricated authority: %+v", rows)
	}
	view.LocalRepositories()
	if stats != 1 {
		t.Fatalf("repeat snapshot probed tree: %d", stats)
	}
	// The host/disk becomes unavailable. This never means remote-only.
	now = now.Add(6 * time.Second)
	view.stat = func(string) (os.FileInfo, error) { stats++; return nil, os.ErrPermission }
	rows = view.LocalRepositories()
	if len(rows) != 1 || rows[0].LocalCopy.Presence != "unavailable" || rows[0].LocalCopy.Path != root {
		t.Fatalf("unavailable location lost: %+v", rows)
	}
	// Removing/revoking the profile cannot resurrect its local presentation.
	active = false
	if rows = view.LocalRepositories(); len(rows) != 0 || len(view.observations) != 0 {
		t.Fatalf("profile survived removal: %+v", rows)
	}
}

func TestLocalCopyPresentationIPCBeforeRuntimeAndAfterProjection(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	record, err := store.BeginAttach("office", "repo-1", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApproveAttach(record.OperationID, "office", "repo-1", "svn+ssh://example/repo-1", "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.MarkAttached(record.OperationID, "repo-1"); err != nil {
		t.Fatal(err)
	}
	socket := shortDaemonSocket(t)
	server := ipcserver.New(socket)
	server.SetLocalRepositorySource(&localCopyPresentation{store: store, active: func(string) bool { return true }})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err = server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client := ipcclient.New(socket, "gui")
	list, err := client.RepoList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Repos) != 1 || list.Repos[0].LocalPath != root || list.Repos[0].Attached || list.Repos[0].LocalCopy.Presence != "present" {
		t.Fatalf("cold list=%+v", list)
	}
	status, err := client.RepoStatus(ctx, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Access != "" || status.Attached {
		t.Fatalf("cold status=%+v", status)
	}
	state := server.RegisterProjectedRepo("repo-1", "Project", "svn+ssh://example/repo-1", "office", "r", contract.StateActive, false)
	list, err = client.RepoList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list.Repos[0].LocalCopy == nil || list.Repos[0].Attached || list.Repos[0].Access != "r" {
		t.Fatalf("projection overwrote local fact: %+v", list)
	}
	state = server.RegisterRepoAccess("repo-1", "svn+ssh://example/repo-1", root, "office", "r")
	state.SetState(contract.StateOffline)
	list, err = client.RepoList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !list.Repos[0].Attached || list.Repos[0].LocalCopy.Presence != "present" || list.Repos[0].State != contract.StateOffline {
		t.Fatalf("offline copy vanished: %+v", list)
	}
}

func TestLocalCopyPresentationKindsAndTerminalIntents(t *testing.T) {
	for _, kind := range []string{"working_copy", "sparse", "virtual"} {
		t.Run(kind, func(t *testing.T) {
			store, err := localrepo.Open(filepath.Join(t.TempDir(), "local.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record localrepo.Record
			root := t.TempDir()
			switch kind {
			case "virtual":
				record, err = store.BeginAnchorAttach("office", "repo", root, false)
			case "sparse":
				record, err = store.BeginSparseAttach("office", "repo", root, "a", false)
			default:
				record, err = store.BeginAttach("office", "repo", root, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.ApproveAttach(record.OperationID, "office", "repo", "svn+ssh://example/repo", "rw"); err != nil {
				t.Fatal(err)
			}
			if _, err = store.MarkAttached(record.OperationID, "repo"); err != nil {
				t.Fatal(err)
			}
			view := &localCopyPresentation{store: store}
			rows := view.LocalRepositories()
			if len(rows) != 1 || rows[0].LocalCopy.Kind != kind {
				t.Fatalf("kind=%s rows=%+v", kind, rows)
			}
			if _, err = store.BeginDetach("office", "repo", false); err != nil {
				t.Fatal(err)
			}
			if rows = view.LocalRepositories(); len(rows) != 0 {
				t.Fatalf("detaching intent resurrected: %+v", rows)
			}
		})
	}
}

func TestLocalCopyPresentationMissingRootIsNotRemote(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "absent")
	record, err := store.BeginAttach("office", "repo", path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApproveAttach(record.OperationID, "office", "repo", "svn+ssh://example/repo", "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.MarkAttached(record.OperationID, "repo"); err != nil {
		t.Fatal(err)
	}
	rows := (&localCopyPresentation{store: store}).LocalRepositories()
	if len(rows) != 1 || rows[0].LocalCopy.Presence != "unavailable" || rows[0].LocalPath != path {
		t.Fatalf("missing location guessed remote: %+v", rows)
	}
}
