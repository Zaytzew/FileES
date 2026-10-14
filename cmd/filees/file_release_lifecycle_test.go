package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"filees/pkg/clientprofile"
	"filees/pkg/commit"
	"filees/pkg/config"
	"filees/pkg/localrepo"
)

func TestFileReleaseIntentExcludesConcurrentLifecycle(t *testing.T) {
	var guard sync.Mutex
	guard.Lock()
	service := repositoryLifecycleService{localIntentMu: &guard}
	if _, err := service.BeginDetach(t.Context(), "server", "repo", false, false); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	if _, err := service.BeginDelete(t.Context(), "server", "repo", "repo"); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	if _, err := service.BeginRelocate("server", "repo", "new", false); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal(err)
	}
	guard.Unlock()
	release, err := service.enterLocalIntent()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !guard.TryLock() {
		t.Fatal("intent lock leaked")
	}
	guard.Unlock()
}

func TestPendingFileReleasePreservesLifecycleAndMetadata(t *testing.T) {
	wc := t.TempDir()
	state := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, commit.FileReleasePendingName), []byte("corrupt proof also fences"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileConfiguredRepositoryLifecycle(store, []config.Repo{{ID: "repo", ServerID: "server", RepoURL: "svn+ssh://_filees-client@example/repo", LocalPath: wc, Access: "rw"}}); err != nil {
		t.Fatal(err)
	}
	service := repositoryLifecycleService{store: store}
	if _, err := service.BeginDetach(t.Context(), "server", "repo", false, false); !errors.Is(err, commit.ErrFileReleasePending) {
		t.Fatal(err)
	}
	if _, err := service.BeginDelete(t.Context(), "server", "repo", "repo"); !errors.Is(err, commit.ErrFileReleasePending) {
		t.Fatal(err)
	}
	if _, err := service.BeginRelocate("server", "repo", filepath.Join(t.TempDir(), "new"), false); !errors.Is(err, commit.ErrFileReleasePending) {
		t.Fatal(err)
	}
	if err := stripWorkingCopyMetadata(wc, "fixture"); !errors.Is(err, commit.ErrFileReleasePending) {
		t.Fatal(err)
	}
	if records := store.List(); len(records) != 1 || records[0].State != localrepo.StateAttached {
		t.Fatal("fence changed lifecycle", records)
	}
	if _, err := os.Stat(filepath.Join(wc, ".svn")); err != nil || !commit.HasPendingFileRelease(wc) {
		t.Fatal("metadata removed", err)
	}
}

func TestPendingFileReleaseAfterQuiescePreservesMetadata(t *testing.T) {
	wc := t.TempDir()
	state := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileConfiguredRepositoryLifecycle(store, []config.Repo{{ID: "repo", ServerID: "server", RepoURL: "svn+ssh://_filees-client@example/repo", LocalPath: wc, Access: "rw"}}); err != nil {
		t.Fatal(err)
	}
	record, err := store.BeginDetach("server", "repo", false)
	if err != nil {
		t.Fatal(err)
	}
	attachments := make(chan provisionedAttachment, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := <-attachments
		// Simulate a writer creating its durable fence while quiesce waited.
		request.Result <- os.WriteFile(filepath.Join(state, commit.FileReleasePendingName), []byte("pending"), 0600)
	}()
	p := daemonProvisioner{local: store, attachments: attachments}
	if _, err := p.runDetach(t.Context(), record, clientprofile.Profile{}); !errors.Is(err, commit.ErrFileReleasePending) {
		t.Fatal(err)
	}
	<-done
	if _, err := os.Stat(filepath.Join(wc, ".svn")); err != nil || !commit.HasPendingFileRelease(wc) {
		t.Fatal("metadata removed after waiting for writer", err)
	}
	current, _ := store.Get(record.OperationID)
	if current.State != localrepo.StateDetaching {
		t.Fatal("detachment falsely completed", current.State)
	}
}
