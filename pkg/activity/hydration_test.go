package activity

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHydrationProtectsLocalPipelineAndDoesNotReplay(t *testing.T) {
	for _, stage := range []Stage{Detected, Pending, Publishing, Failed, Received, Published, Reconciled} {
		t.Run(string(stage), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "activity.json")
			j, err := Open(file, 20)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			j.now = func() time.Time { return now }
			old := Entry{RepoID: "repo", Path: "a.dwg", Kind: Added, Stage: stage}
			if stage == Received || stage == Published {
				old.Revision = 7
			}
			if stage == Failed {
				old.ErrorID = "test"
			}
			if err := j.Record(old); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(file)
			now = now.Add(time.Hour)
			changed, err := j.RecordHydration(Entry{RepoID: "repo", Path: "a.dwg", Kind: Modified, Stage: Received, Revision: 7})
			if err != nil || changed != (stage == Reconciled) {
				t.Fatal(changed, err)
			}
			after, _ := os.ReadFile(file)
			if stage != Reconciled && string(before) != string(after) {
				t.Fatal("existing work/receipt overwritten")
			}
			j, err = Open(file, 20)
			if err != nil {
				t.Fatal(err)
			}
			changed, err = j.RecordHydration(Entry{RepoID: "repo", Path: "a.dwg", Kind: Modified, Stage: Received, Revision: 7})
			if err != nil || changed {
				t.Fatal("restart replay", changed, err)
			}
		})
	}
}

func TestHydrationRecordsNewRevisionOnceUnderConcurrentACKs(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "activity.json"), 20)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	changed := make(chan bool, 32)
	for range 32 {
		wg.Go(func() {
			n := int64(123)
			ok, err := j.RecordHydration(Entry{RepoID: "repo", Path: "a.dwg", Kind: Modified, Stage: Received, Revision: 9, Size: &n})
			if err != nil {
				t.Error(err)
			}
			changed <- ok
		})
	}
	wg.Wait()
	close(changed)
	count := 0
	for ok := range changed {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate change events", count)
	}
	for _, revision := range []int64{8, 9, 10} {
		ok, err := j.RecordHydration(Entry{RepoID: "repo", Path: "a.dwg", Kind: Modified, Stage: Received, Revision: revision})
		if err != nil || ok != (revision == 10) {
			t.Fatal(revision, ok, err)
		}
	}
	if row := j.List()[0]; row.Revision != 10 || row.Stage != Received {
		t.Fatal(row)
	}
}

func TestHydrationRejectsInvalidReceipt(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "activity.json"), 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{
		{RepoID: "repo", Path: "file", Kind: Modified, Stage: Published, Revision: 1},
		{RepoID: "repo", Path: "file", Kind: Modified, Stage: Received},
		{RepoID: "repo", Path: "../escape", Kind: Modified, Stage: Received, Revision: 1},
	} {
		if changed, err := j.RecordHydration(entry); err == nil || changed {
			t.Fatal(entry, changed, err)
		}
	}
}
