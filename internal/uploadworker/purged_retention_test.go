package uploadworker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgedLogRetentionKeepsBoundaryAndFuture(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, item := range []PurgedItem{{UploadID: "old", PurgedAt: now.Add(-WaitingRoomTTL - time.Nanosecond)}, {UploadID: "boundary", PurgedAt: now.Add(-WaitingRoomTTL)}, {UploadID: "fresh", PurgedAt: now}, {UploadID: "future", PurgedAt: now.Add(time.Hour)}} {
		if e := appendPurgedLog(root, item); e != nil {
			t.Fatal(e)
		}
	}
	if e := (Reaper{TrashRoot: root}).PurgeExpired(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(root, purgedLogName))
	if e != nil || bytes.Contains(raw, []byte(`"old"`)) || bytes.Count(raw, []byte("\n")) != 3 {
		t.Fatal("retention failed", e)
	}
	items, e := readPurgedLog(root, now)
	if e != nil || len(items) != 3 {
		t.Fatal("visible boundary changed", e)
	}
	if e := (Reaper{TrashRoot: root}).PurgeExpired(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(filepath.Join(root, purgedLogName))
	if e != nil || !bytes.Equal(raw, after) {
		t.Fatal("unchanged log modified", e)
	}
	if temps, _ := filepath.Glob(filepath.Join(root, ".purged-log-*.tmp")); len(temps) != 0 {
		t.Fatal("temp residue")
	}
}

func TestPurgedLogRetentionPreservesMalformedEvidence(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	path := filepath.Join(root, purgedLogName)
	unknown := "truncated historical line\n{\"upload_id\":\"missing-date\"}\n{\"upload_id\":\"zero-date\",\"purged_at\":\"0001-01-01T00:00:00Z\"}\n"
	if e := os.WriteFile(path, []byte(unknown), 0600); e != nil {
		t.Fatal(e)
	}
	if e := appendPurgedLog(root, PurgedItem{UploadID: "old", PurgedAt: now.Add(-72 * time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if e := compactPurgedLog(root, now); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || string(raw) != unknown {
		t.Fatal("unknown-age evidence lost", e)
	}
	items, e := readPurgedLog(root, now)
	if e != nil || len(items) != 0 {
		t.Fatal("malformed record visible", e)
	}
}
