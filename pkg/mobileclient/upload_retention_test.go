package mobileclient

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDeliveredRetentionRequiresConsumedSources(t *testing.T) {
	s := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	makeItem := func(state UploadState, ack time.Time, sources []string) PendingUpload {
		item := PendingUpload{ID: uuid.NewString(), RepoID: "repo-1", State: state, EnqueuedAt: old, Sources: sources, SourcesAcknowledgedAt: ack}
		if err := s.recordUploadOutcome(item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	unconsumed := makeItem(UploadCommitted, time.Time{}, []string{"source"})
	consumed := makeItem(UploadCommitted, old, []string{"source"})
	duplicate := makeItem(UploadDroppedSame, old, []string{"source"})
	noSources := makeItem(UploadCommitted, time.Time{}, nil)
	recent := makeItem(UploadCommitted, now.Add(-time.Hour), []string{"source"})
	future := makeItem(UploadCommitted, now.Add(time.Hour), []string{"source"})
	kept := []PendingUpload{unconsumed, recent, future}
	for _, state := range []UploadState{UploadConflict, UploadParked, UploadUploading, UploadPendingCreate} {
		item := makeItem(state, old, []string{"source"})
		kept = append(kept, item)
		if err := os.WriteFile(s.uploadPayloadPath(item.RepoID, item.ID), []byte("pending bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	zero := makeItem(UploadCommitted, old, nil)
	zero.EnqueuedAt = time.Time{}
	if err := s.recordUploadOutcome(zero); err != nil {
		t.Fatal(err)
	}
	kept = append(kept, zero)
	other := consumed
	other.RepoID = "repo-2"
	if err := s.recordUploadOutcome(other); err != nil {
		t.Fatal(err)
	}
	// Simulate failure to free the payload after persisting a delivered receipt.
	if err := os.WriteFile(s.uploadPayloadPath(consumed.RepoID, consumed.ID), []byte("delivered bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListUploads("repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.pruneDeliveredUploads("repo-1", items, now); err != nil {
		t.Fatal(err)
	}
	for _, item := range []PendingUpload{consumed, duplicate, noSources} {
		if _, err := os.Stat(s.uploadMetaPath(item.RepoID, item.ID)); !os.IsNotExist(err) {
			t.Fatalf("kept expired receipt %s: %v", item.ID, err)
		}
		if _, err := os.Stat(s.uploadPayloadPath(item.RepoID, item.ID)); !os.IsNotExist(err) {
			t.Fatalf("orphaned payload %s: %v", item.ID, err)
		}
	}
	for _, item := range append(kept, other) {
		if _, err := s.loadUploadMeta(item.RepoID, item.ID); err != nil {
			t.Fatalf("removed protected receipt: %v", err)
		}
		if !delivered(item.State) {
			if data, err := s.loadUploadPayload(item.RepoID, item.ID); err != nil || string(data) != "pending bytes" {
				t.Fatal("lost pending payload", err)
			}
		}
	}
}

func TestDeliveredAcknowledgementRestartAndBoundary(t *testing.T) {
	ctx := context.Background()
	s := Store{Root: t.TempDir()}
	item := PendingUpload{ID: uuid.NewString(), RepoID: "repo-1", State: UploadCommitted,
		EnqueuedAt: time.Now().UTC().Add(-365 * 24 * time.Hour), Sources: []string{"a", "b"}}
	if err := s.recordUploadOutcome(item); err != nil {
		t.Fatal(err)
	}
	// A year offline does not permit losing the receipt before seen recovery.
	if err := s.PruneDeliveredUploads(ctx, item.RepoID); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeUploadSources(ctx, item.RepoID, item.ID); err != nil {
		t.Fatal(err)
	}
	reopened := Store{Root: s.Root}
	ack, err := reopened.loadUploadMeta(item.RepoID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ack.SourcesAcknowledgedAt.IsZero() {
		t.Fatal("ACK was not persisted")
	}
	if err := reopened.AcknowledgeUploadSources(ctx, item.RepoID, item.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := reopened.loadUploadMeta(item.RepoID, item.ID)
	if !again.SourcesAcknowledgedAt.Equal(ack.SourcesAcknowledgedAt) {
		t.Fatal("repeated ACK extended retention")
	}
	boundary := ack.SourcesAcknowledgedAt.Add(deliveredRetention)
	if err := reopened.pruneDeliveredUploads(item.RepoID, []PendingUpload{ack}, boundary.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.loadUploadMeta(item.RepoID, item.ID); err != nil {
		t.Fatal("pruned early", err)
	}
	if err := reopened.pruneDeliveredUploads(item.RepoID, []PendingUpload{ack}, boundary); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.loadUploadMeta(item.RepoID, item.ID); !os.IsNotExist(err) {
		t.Fatal("receipt not pruned", err)
	}
}

func TestDeliveredRetentionRefusesPendingAckAndInvalidScope(t *testing.T) {
	s := Store{Root: t.TempDir()}
	ctx := context.Background()
	item, err := s.EnqueueUpload("repo-1", "mobile-uploads", "a.txt", "", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeUploadSources(ctx, item.RepoID, item.ID); err == nil {
		t.Fatal("acknowledged pending upload")
	}
	for _, bad := range []string{"", ".", "..", "../outside", "..\\outside", "C:"} {
		if err := s.PruneDeliveredUploads(ctx, bad); err == nil {
			t.Fatalf("accepted repo %q", bad)
		}
		if err := s.AcknowledgeUploadSources(ctx, item.RepoID, bad); err == nil {
			t.Fatalf("accepted ID %q", bad)
		}
	}
}

func TestEnqueuePrunesOnlyAcknowledgedHistory(t *testing.T) {
	s := Store{Root: t.TempDir()}
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	item := PendingUpload{ID: uuid.NewString(), RepoID: "repo-1", State: UploadCommitted, EnqueuedAt: old, SourcesAcknowledgedAt: old, Sources: []string{"source"}}
	if err := s.recordUploadOutcome(item); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueUpload("repo-1", "mobile-uploads", "new.txt", "", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadUploadMeta(item.RepoID, item.ID); !os.IsNotExist(err) {
		t.Fatal("enqueue retained expired receipt", err)
	}
}
