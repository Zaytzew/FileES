package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/commit"
	"filees/pkg/config"
	"filees/pkg/localrepo"
)

func TestRemoteTreeFencePreservesLifecycleIncludingNocfapi(t *testing.T) {
	for _, fence := range []struct {
		name string
		err  error
	}{{commit.RemoteTreePendingName, commit.ErrRemoteTreePending}, {commit.RemoteFileDeletePendingName, commit.ErrRemoteFileDeletePending}} {
		t.Run(fence.name, func(t *testing.T) {
			wc := t.TempDir()
			state := filepath.Join(wc, ".filees", "state")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(wc, ".svn"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, fence.name), []byte("corrupt proof remains fenced"), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reconcileConfiguredRepositoryLifecycle(store, []config.Repo{{ID: "repo", ServerID: "server", RepoURL: "svn+ssh://user@example.test/repo", LocalPath: wc, Access: "rw"}}); err != nil {
				t.Fatal(err)
			}
			s := repositoryLifecycleService{store: store}
			if _, err := s.BeginDetach(t.Context(), "server", "repo", false, false); !errors.Is(err, fence.err) {
				t.Fatal(err)
			}
			if _, err := s.BeginDelete(t.Context(), "server", "repo", "repo"); !errors.Is(err, fence.err) {
				t.Fatal(err)
			}
			if _, err := s.BeginRelocate("server", "repo", filepath.Join(t.TempDir(), "new"), true); !errors.Is(err, fence.err) {
				t.Fatal(err)
			}
			if err := stripWorkingCopyMetadata(wc, "fixture"); !errors.Is(err, fence.err) {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(wc, ".svn")); err != nil || !commit.HasPendingLocalMutation(wc) {
				t.Fatal("fence/metadata removed", err)
			}
			if records := store.List(); len(records) != 1 || records[0].State != localrepo.StateAttached {
				t.Fatal(records)
			}
		})
	}
}
