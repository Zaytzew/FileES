package activity

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestSessionRetentionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.json")
	j, e := Open(path, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.Record(Entry{RepoID: "repo", Path: "file", Kind: Added, Stage: Pending}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 25; i++ {
		if e = j.RecordSession(SessionEvent{ID: fmt.Sprint(i), Phase: "started", Version: "dev"}); e != nil {
			t.Fatal(e)
		}
	}
	if e = j.RecordSession(SessionEvent{ID: "24", Phase: "started"}); e != nil {
		t.Fatal(e)
	}
	j, e = Open(path, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(j.Sessions()) != 20 || j.Sessions()[0].ID != "5" || len(j.List()) != 1 {
		t.Fatal("retention or duplicate", j.Sessions())
	}
	events := j.Sessions()
	events[0].ID = "changed"
	if j.Sessions()[0].ID == "changed" {
		t.Fatal("mutable snapshot")
	}
	if j.RecordSession(SessionEvent{ID: "bad", Phase: "completed"}) == nil {
		t.Fatal("invented completion")
	}
	// Failed persistence must not poison deduplication of a subsequent attempt.
	original := j.path
	j.path = t.TempDir()
	if j.RecordSession(SessionEvent{ID: "stop", Phase: "stopping"}) == nil {
		t.Fatal("expected persistence failure")
	}
	j.path = original
	if e = j.RecordSession(SessionEvent{ID: "stop", Phase: "stopping"}); e != nil {
		t.Fatal(e)
	}
	j, e = Open(path, 1)
	if e != nil || j.Sessions()[19].ID != "stop" {
		t.Fatal("lost retry", e)
	}
}
