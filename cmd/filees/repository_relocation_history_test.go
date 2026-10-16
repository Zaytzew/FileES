package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/localrepo"
)

func lifecycleWithEarlierDetachedRecord(t *testing.T) (*localrepo.Store, localrepo.Record) {
	t.Helper()
	base := t.TempDir()
	wc := filepath.Join(base, "live")
	if err := os.MkdirAll(filepath.Join(wc, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	live := localrepo.Record{OperationID: "ffffffff-ffff-4fff-8fff-ffffffffffff", ServerID: "spot", RepoID: "repo", RepoURL: "svn+ssh://example/repo", Access: "rw", State: localrepo.StateAttached, LocalPath: wc, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	old := live
	old.OperationID, old.DetachOperationID, old.State = "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", localrepo.StateDetached
	raw, err := json.Marshal(struct {
		Schema  string             `json:"schema"`
		Records []localrepo.Record `json:"records"`
	}{localrepo.Schema, []localrepo.Record{old, live}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "state.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := localrepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, live
}

func TestRelocationPreflightUsesCurrentAttachmentAndReplaysItsQueue(t *testing.T) {
	s, live := lifecycleWithEarlierDetachedRecord(t)
	scheduled := []string{}
	service := repositoryLifecycleService{store: s, onRelocate: func(id string) { scheduled = append(scheduled, id) }}
	target := filepath.Join(filepath.Dir(live.LocalPath), "target")
	first, err := service.BeginRelocate(live.ServerID, live.RepoID, target, true)
	if err != nil || first.OperationID != live.OperationID {
		t.Fatal(first, err)
	}
	// The executor may have created the target before a repeated click arrives.
	// Admission must recognize this exact current intent before preflight.
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	again, err := service.BeginRelocate(live.ServerID, live.RepoID, target, true)
	if err != nil || again.OperationID != live.OperationID {
		t.Fatal(again, err)
	}
	if len(scheduled) != 2 || scheduled[0] != live.OperationID || scheduled[1] != live.OperationID {
		t.Fatal(scheduled)
	}
}

func TestLocatePreflightExcludesCurrentNotHistoricalAttachment(t *testing.T) {
	s, live := lifecycleWithEarlierDetachedRecord(t)
	service := repositoryLifecycleService{store: s}
	got, err := service.BeginLocate(live.ServerID, live.RepoID, live.LocalPath)
	if err != nil || got.OperationID != live.OperationID {
		t.Fatal(got, err)
	}
}
