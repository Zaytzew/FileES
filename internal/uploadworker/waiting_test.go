package uploadworker

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWaitingListHidesAndPurgesAfterTTL(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	freshID, oldID, hiddenID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	writeWaiting(t, root, "shelf-a/"+freshID[:8]+"/2026-08-29/"+freshID, Index{
		UploadID: freshID, OriginalName: "nowy.pdf", Size: 10, ReceivedAt: now.Add(-2 * time.Hour),
	}, "fresh")
	writeWaiting(t, root, "shelf-a/"+oldID[:8]+"/2026-08-27/"+oldID, Index{
		UploadID: oldID, OriginalName: "stary.pdf", Size: 11, ReceivedAt: now.Add(-49 * time.Hour),
	}, "old")
	writeWaiting(t, root, "shelf-a/"+hiddenID[:8]+"/2026-08-29/"+hiddenID, Index{
		UploadID: hiddenID, OriginalName: "ukryty.pdf", Size: 12, ReceivedAt: now.Add(-time.Hour), Hidden: true,
	}, "hidden")
	reaper := Reaper{TrashRoot: root, Now: func() time.Time { return now }}
	list, err := reaper.ListWaiting("", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 1 || list.Entries[0].UploadID != freshID || list.Entries[0].RemainingHours != 46 {
		t.Fatalf("entries=%+v", list.Entries)
	}
	if len(list.Purged) != 1 || list.Purged[0].OriginalName != "stary.pdf" {
		t.Fatalf("purged=%+v", list.Purged)
	}
	if _, err := os.Stat(filepath.Join(root, "shelf-a", oldID[:8], "2026-08-27", oldID)); !os.IsNotExist(err) {
		t.Fatalf("expired waiting room remained: %v", err)
	}
	if err := reaper.HideWaiting(freshID, now); err != nil {
		t.Fatal(err)
	}
	list, err = reaper.ListWaiting("", now)
	if err != nil || len(list.Entries) != 0 {
		t.Fatalf("hidden still listed: %+v %v", list.Entries, err)
	}
	_, raw, hours, err := reaper.FetchWaiting(hiddenID, now)
	if err == nil || raw != nil || hours != 0 {
		t.Fatalf("hidden fetch: %v hours=%d", err, hours)
	}
}

func TestSeedRejectAppearsOnOwnerList(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	owner := uuid.NewString()
	reaper := Reaper{TrashRoot: root}
	idx, err := reaper.SeedReject(owner, "eicar.com", now)
	if err != nil || idx.OwnerRealm != owner || idx.AVVerdict == "" || idx.Size == 0 {
		t.Fatalf("seed idx=%+v err=%v", idx, err)
	}
	list, err := reaper.ListWaiting(owner, now)
	if err != nil || len(list.Entries) != 1 || list.Entries[0].UploadID != idx.UploadID || list.Entries[0].RemainingHours != 48 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	_, raw, hours, err := reaper.FetchWaiting(idx.UploadID, now)
	if err != nil || hours != 48 || len(raw) == 0 {
		t.Fatalf("fetch raw=%d hours=%d err=%v", len(raw), hours, err)
	}
}

func TestFetchWaitingReturnsPayloadAndRemainingHours(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	id := uuid.NewString()
	writeWaiting(t, root, "shelf/"+id[:8]+"/2026-08-29/"+id, Index{
		UploadID: id, OriginalName: "Opinia.pdf", Size: 4, ReceivedAt: now.Add(-3 * time.Hour),
	}, "plik")
	reaper := Reaper{TrashRoot: root}
	idx, raw, hours, err := reaper.FetchWaiting(id, now)
	if err != nil || string(raw) != "plik" || hours != 45 || idx.OriginalName != "Opinia.pdf" {
		t.Fatalf("fetch idx=%+v raw=%q hours=%d err=%v", idx, raw, hours, err)
	}
}

func writeWaiting(t *testing.T, root, rel string, idx Index, payload string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, payloadName), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeIndex(filepath.Join(dir, indexName), idx); err != nil {
		t.Fatal(err)
	}
}
