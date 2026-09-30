package reservationclient

import (
	v1 "filees/pkg/reservation/v1"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageLatchRetainsRiskUntilFreshRecovery(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "view.json")
	now := time.Now()
	r := v1.Result{Schema: v1.AutolockSchema, RepoID: testRepoID, RepositoryState: "active", StorageWrite: &v1.StorageWrite{State: "blocked", MeasuredAt: &now, ValidForSeconds: 180}}
	check := func(want bool) {
		t.Helper()
		got, err := StorageBlocked(cache, "server", testRepoID)
		if err != nil || got != want {
			t.Fatal(got, err)
		}
	}
	check(false)
	if err := ObserveStorage(cache, "server", testRepoID, r, 0); err != nil {
		t.Fatal(err)
	}
	check(true)
	// No in-memory object: this same read is used after a process restart.
	for _, signal := range []*v1.StorageWrite{nil, {State: "unknown"}, {State: "available", MeasuredAt: &now, ValidForSeconds: 1}} {
		r.StorageWrite = signal
		if err := ObserveStorage(cache, "server", testRepoID, r, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		check(true)
	}
	if held, err := StorageBlocked(cache, "other-server", testRepoID); held || err != nil {
		t.Fatal("cross-server hold", err)
	}
	if held, err := StorageBlocked(cache, "server", "other-repo"); held || err != nil {
		t.Fatal("cross-repo hold", err)
	}
	r.StorageWrite = &v1.StorageWrite{State: "available", MeasuredAt: &now, ValidForSeconds: 180}
	if err := ObserveStorage(cache, "server", testRepoID, r, time.Second); err != nil {
		t.Fatal(err)
	}
	check(false)
	// Malformed local state cannot silently enable writes.
	p, _ := storageLatchPath(cache, "server", testRepoID)
	if err := os.WriteFile(p, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if held, err := StorageBlocked(cache, "server", testRepoID); !held || err == nil {
		t.Fatal(held, err)
	}
	r.RepoID = "other-repo"
	if err := ObserveStorage(cache, "server", testRepoID, r, 0); err == nil {
		t.Fatal("foreign response cleared hold")
	}
}

func TestStorageLatchPersistenceFailureIsNotSuccess(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "storage-holds"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	r := v1.Result{Schema: v1.StateSchema, RepoID: testRepoID, RepositoryState: "active", StorageWrite: &v1.StorageWrite{State: "blocked"}}
	if err := ObserveStorage(filepath.Join(root, "view.json"), "server", testRepoID, r, 0); err == nil {
		t.Fatal("ignored persistence failure")
	}
}
