//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func TestAnchorDownloadUnavailableDoesNotBecomeOrdinarySparse(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	record, err := store.BeginAnchorAttach("server", "repo", local, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveAttach(record.OperationID, "server", "repo", "svn+ssh://example/repo", "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAttached(record.OperationID, "repo"); err != nil {
		t.Fatal(err)
	}
	server := ipcserver.New("unused")
	server.RegisterRepoAccess("repo", "svn+ssh://example/repo", local, "server", "rw")
	m := &anchorManager{lifecycle: store, repos: server.RepoState}
	if handled, err := m.download(t.Context(), "server", "repo", "file.txt"); !handled || !errors.Is(err, ipcserver.ErrDepthUnavailable) {
		t.Fatalf("missing helper allowed SVN fallback: handled=%v err=%v", handled, err)
	}
	other := server.RegisterRepoAccess("ordinary", "svn://example/ordinary", t.TempDir(), "server", "rw")
	if handled, err := m.download(t.Context(), "server", other.Summary().ID, "file.txt"); handled || err != nil {
		t.Fatalf("ordinary WC intercepted: handled=%v err=%v", handled, err)
	}
	if _, err := store.BeginDetach("server", "repo", false); err != nil {
		t.Fatal(err)
	}
	if handled, err := m.download(t.Context(), "server", "repo", "file.txt"); !handled || err == nil {
		t.Fatalf("detaching point allowed download: handled=%v err=%v", handled, err)
	}
}

func TestAnchorDownloadWaitsForConfirmedAdoption(t *testing.T) {
	local := t.TempDir()
	server := ipcserver.New("unused")
	repo := server.RegisterRepoAccess("repo", "svn://example/repo", local, "server", "rw")
	m := &anchorManager{repos: server.RepoState}
	record := localrepo.Record{ServerID: "server", RepoID: "repo", LocalPath: local}
	calls := 0
	repo.SetLocalStatusFunc(func(_ context.Context, paths []string, confirm func([]client.StatusEntry) error) error {
		calls++
		if len(paths) != 1 || paths[0] != "file.txt" {
			t.Fatalf("paths=%v", paths)
		}
		switch calls {
		case 1:
			return commit.ErrLocalStatusBusy
		case 2:
			return confirm([]client.StatusEntry{{Path: "file.txt", Item: "unversioned"}})
		case 3:
			return confirm(nil)
		default:
			return confirm([]client.StatusEntry{{Path: filepath.Join(local, "file.txt"), Item: "modified"}})
		}
	})
	if err := m.waitDownloaded(t.Context(), record, "file.txt"); err != nil || calls != 4 {
		t.Fatalf("adoption calls=%d err=%v", calls, err)
	}
	failed := errors.New("status failed")
	repo.SetLocalStatusFunc(func(context.Context, []string, func([]client.StatusEntry) error) error { return failed })
	if err := m.waitDownloaded(t.Context(), record, "file.txt"); !errors.Is(err, failed) {
		t.Fatalf("status error hidden: %v", err)
	}
	repo.SetLocalStatusFunc(func(context.Context, []string, func([]client.StatusEntry) error) error { return nil })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := m.waitDownloaded(ctx, record, "file.txt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unconfirmed download succeeded: %v", err)
	}
}
