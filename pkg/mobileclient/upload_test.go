package mobileclient

import (
	"context"
	"os"
	"testing"
)

func TestDrainPendingCommitsNewUpload(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")

	item, err := c.Store.EnqueueUpload("repo-1", "photos/2026", "new.txt", "text/plain", []byte("brand new content"))
	if err != nil {
		t.Fatal(err)
	}
	if item.State != UploadPendingCreate {
		t.Fatalf("initial state = %v", item.State)
	}

	results, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	got := results[0]
	if got.State != UploadCommitted {
		t.Fatalf("state = %v, outcome = %v, lastError = %q", got.State, got.Outcome, got.LastError)
	}
	if got.Revision == 0 || got.FinalPath != "photos/2026/new.txt" {
		t.Fatalf("revision/final_path = %d/%q", got.Revision, got.FinalPath)
	}
	if _, err := os.Stat(c.Store.uploadPayloadPath("repo-1", item.ID)); !os.IsNotExist(err) {
		t.Fatalf("payload should be freed after commit, stat err = %v", err)
	}

	// A second drain must not resend an already-terminal item.
	again, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].State != UploadCommitted {
		t.Fatalf("second drain = %+v", again)
	}
}

func TestDrainPendingDropsIdenticalDuplicate(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")

	item, err := c.Store.EnqueueUpload("repo-1", "photos/2026", "a.jpg", "image/jpeg", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	results, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	got := results[0]
	if got.State != UploadDroppedSame {
		t.Fatalf("state = %v, outcome = %v", got.State, got.Outcome)
	}
	if _, err := os.Stat(c.Store.uploadPayloadPath("repo-1", item.ID)); !os.IsNotExist(err) {
		t.Fatalf("payload should be freed after dedup drop, stat err = %v", err)
	}
}

func TestDrainPendingParksConflictingDuplicate(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")

	item, err := c.Store.EnqueueUpload("repo-1", "photos/2026", "a.jpg", "image/jpeg", []byte("different content"))
	if err != nil {
		t.Fatal(err)
	}

	results, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	got := results[0]
	if got.State != UploadConflict {
		t.Fatalf("state = %v, outcome = %v", got.State, got.Outcome)
	}
	if got.ExistingSha256 == "" {
		t.Fatal("expected existing_sha256 to be set on conflict")
	}
	// A conflict is parked, not resolved: the payload must survive so the
	// caller can still retry under a new name or explicitly discard it.
	if _, err := os.Stat(c.Store.uploadPayloadPath("repo-1", item.ID)); err != nil {
		t.Fatalf("payload should survive a conflict, stat err = %v", err)
	}

	// A conflict is terminal from DrainPending's point of view: it must not
	// be resent automatically.
	again, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if again[0].State != UploadConflict {
		t.Fatalf("second drain = %+v", again)
	}
}

func TestDrainPendingParksDestinationGone(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "rw")

	item, err := c.Store.EnqueueUpload("repo-1", "no/such/parent", "new.txt", "text/plain", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	results, err := c.DrainPending(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	got := results[0]
	if got.State != UploadParked || got.Outcome != "DESTINATION_GONE" {
		t.Fatalf("state = %v, outcome = %v", got.State, got.Outcome)
	}
	if _, err := os.Stat(c.Store.uploadPayloadPath("repo-1", item.ID)); err != nil {
		t.Fatalf("parked payload should survive, stat err = %v", err)
	}
}

func TestDiscardUpload(t *testing.T) {
	c := Client{Store: Store{Root: t.TempDir()}}

	item, err := c.Store.EnqueueUpload("repo-1", "photos", "x.bin", "", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.DiscardUpload("repo-1", item.ID); err != nil {
		t.Fatal(err)
	}
	items, err := c.Store.ListUploads("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items after discard = %+v", items)
	}
	if _, err := os.Stat(c.Store.uploadPayloadPath("repo-1", item.ID)); !os.IsNotExist(err) {
		t.Fatalf("discarded payload should be gone, stat err = %v", err)
	}
}

func TestListUploadsOrdersByEnqueueTime(t *testing.T) {
	s := Store{Root: t.TempDir()}
	first, err := s.EnqueueUpload("repo-1", "p", "first.bin", "", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnqueueUpload("repo-1", "p", "second.bin", "", []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListUploads("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != first.ID || items[1].ID != second.ID {
		t.Fatalf("items = %+v (first=%s second=%s)", items, first.ID, second.ID)
	}
}

func TestListUploadsEmptyRepoReturnsNil(t *testing.T) {
	s := Store{Root: t.TempDir()}
	items, err := s.ListUploads("no-such-repo")
	if err != nil || len(items) != 0 {
		t.Fatalf("items = %+v err = %v", items, err)
	}
}
