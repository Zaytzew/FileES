package commit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/activity"
	"filees/pkg/client"
	"filees/pkg/watcher"
)

func TestReceivedRemovalRequiresReceiptAndAbsentUnversionedPath(t *testing.T) {
	for _, tc := range []struct {
		name, item, props                                                    string
		noReceipt, exists, recreate, fail, wrongPath, pending, empty, accept bool
	}{
		{name: "native none", item: "none", props: "none", accept: true},
		{name: "CLI unversioned", item: "unversioned", props: "none", accept: true},
		{name: "CLI empty", empty: true, accept: true},
		{name: "no origin", item: "none", props: "none", noReceipt: true},
		{name: "local missing", item: "missing", props: "none"},
		{name: "scheduled delete", item: "deleted", props: "none"},
		{name: "unknown props", item: "none"},
		{name: "wrong path", item: "none", props: "none", wrongPath: true},
		{name: "failed status", fail: true},
		{name: "recreated before check", item: "none", props: "none", exists: true},
		{name: "recreated during check", item: "none", props: "none", recreate: true},
		{name: "already queued", item: "none", props: "none", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wc := t.TempDir()
			path := filepath.Join(wc, "file.txt")
			cli := &cleanPendingClient{revisionClient: &revisionClient{status: []client.StatusEntry{{Path: "file.txt", Item: tc.item, Props: tc.props}}}}
			if tc.empty {
				cli.status = nil
			}
			if tc.wrongPath {
				cli.status[0].Path = "other.txt"
			}
			if tc.fail {
				cli.statusErr = errors.New("status failed")
			}
			create := func() {
				t.Helper()
				if err := os.WriteFile(path, []byte("local"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.exists {
				create()
			}
			if tc.recreate {
				cli.during = create
			}
			s := &Service{Cli: cli, wc: wc, staging: make(map[string]*stageItem), receivedDeletes: map[string]bool{"file.txt": !tc.noReceipt}}
			if tc.pending {
				s.staging["file.txt"] = &stageItem{Op: watcher.Deleted}
			}
			if got := s.receivedRemoval(t.Context(), "file.txt"); got != tc.accept {
				t.Fatalf("accepted=%v, want %v", got, tc.accept)
			}
		})
	}
}

func TestReceivedRemovalSurvivesRestartAndRepeatedDelayedEvents(t *testing.T) {
	root := t.TempDir()
	journalPath := filepath.Join(root, "activity.json")
	journal, err := activity.Open(journalPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(activity.Entry{RepoID: "repo", Path: "old.txt", Kind: activity.Deleted, Stage: activity.Received, Revision: 9}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		reopened, err := activity.Open(journalPath, 100)
		if err != nil {
			t.Fatal(err)
		}
		s := &Service{Cli: &revisionClient{status: []client.StatusEntry{{Path: "old.txt", Item: "none", Props: "none"}}}, wc: root, repoID: "repo", Activity: reopened, staging: make(map[string]*stageItem)}
		for replay := 0; replay < 2; replay++ {
			s.acceptEvents(t.Context(), []watcher.Event{{Rel: "old.txt", Path: filepath.Join(root, "old.txt"), Op: watcher.Deleted, Type: watcher.EntryFile}})
		}
		if s.stagingLen() != 0 {
			t.Fatal("received delete entered outgoing queue")
		}
		entries := reopened.List()
		if len(entries) != 1 || entries[0].Stage != activity.Received || entries[0].Revision != 9 {
			t.Fatalf("lost receipt: %+v", entries)
		}
	}
	for _, stage := range []activity.Stage{activity.Pending, activity.Published, activity.Reconciled} {
		var revision int64
		if stage == activity.Published {
			revision = 9
		}
		if err := journal.Record(activity.Entry{RepoID: "repo", Path: "old.txt", Kind: activity.Deleted, Stage: stage, Revision: revision}); err != nil {
			t.Fatal(err)
		}
		s := &Service{Cli: &revisionClient{}, wc: root, repoID: "repo", Activity: journal}
		if s.receivedRemoval(t.Context(), "old.txt") {
			t.Fatalf("%s is not an incoming receipt", stage)
		}
	}
}

func TestReceivedRemovalDirectoryReceipt(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, rel := range []string{"old/deeper/file.txt", "old-other/file.txt"} {
			root := t.TempDir()
			s := &Service{Cli: &revisionClient{status: []client.StatusEntry{{Path: rel, Item: "none", Props: "none"}}}, wc: root, repoID: "repo"}
			if durable {
				p := filepath.Join(root, "activity.json")
				j, err := activity.Open(p, 20)
				if err != nil {
					t.Fatal(err)
				}
				if err := j.Record(activity.Entry{RepoID: "repo", Path: "old", Kind: activity.Deleted, Stage: activity.Received, Revision: 9}); err != nil {
					t.Fatal(err)
				}
				s.Activity, err = activity.Open(p, 20)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				s.receivedDeletes = map[string]bool{"old": true}
			}
			if got := s.receivedRemoval(t.Context(), rel); got != (rel == "old/deeper/file.txt") {
				t.Fatalf("durable=%v rel=%s accepted=%v", durable, rel, got)
			}
		}
	}
}
