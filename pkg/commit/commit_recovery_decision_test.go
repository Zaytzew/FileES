package commit

import (
	"filees/pkg/activity"
	"os"
	"path/filepath"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
	"github.com/google/uuid"
)

func TestVanishedAddsDoNotLeaveParentOnlyCommitTargets(t *testing.T) {
	wc := t.TempDir()
	paths := []string{"large/move/first.txt", "large/move/second.txt"}
	for _, rel := range paths {
		abs := filepath.Join(wc, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Model the stress case: both files move again after status/staging but
	// before the final publication snapshot is built.
	for _, rel := range paths {
		if err := os.Remove(filepath.Join(wc, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	adds, parents := publishableAddsAndParents(wc, paths)
	if len(adds) != 0 || len(parents) != 0 {
		t.Fatalf("vanished additions produced commit targets: adds=%v parents=%v", adds, parents)
	}
}

func TestVanishedRenameDestinationsDoNotLeaveParentOnlyCommitTargets(t *testing.T) {
	wc := t.TempDir()
	items := []*stageItem{{Rel: "large/move/final.txt", OldRel: "old/final.txt", Op: watcher.Renamed}}
	kept, parents := publishableRenamesAndParents(wc, items)
	if len(kept) != 0 || len(parents) != 0 {
		t.Fatalf("vanished rename produced commit targets: renames=%v parents=%v", kept, parents)
	}
}

func TestCommitRecoveryDecisionRetiresOnlyProvenNoEffectAttempt(t *testing.T) {
	s, c, _, wc := transactionFixture(t)
	in := &commitIntent{Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: wc, Phase: "attempting", FirstRevision: 5, Paths: []string{"a.txt"}, BusyMarker: "transaction=test\npid=1\n"}
	if err := s.writeStateString(filepath.Join(wc, ".filees", "state", "commit.busy"), in.BusyMarker); err != nil {
		t.Fatal(err)
	}
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanCommitRecovery(t.Context())
	if err != nil || plan.TransactionID != in.ID || plan.HeadRevision != 4 || plan.Choice != contract.CommitRecoveryRetryQueue {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	result, err := s.ApplyCommitRecovery(t.Context(), plan.PlanID, plan.Choice)
	if err != nil || result.State != "queued" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	stored, err := s.readIntent(wc)
	if err != nil || stored.Phase != "done" || len(s.staging) != 1 {
		t.Fatalf("intent=%+v staging=%d err=%v", stored, len(s.staging), err)
	}
	if _, err := os.Stat(filepath.Join(wc, ".filees", "state", "commit.busy")); !os.IsNotExist(err) {
		t.Fatalf("busy marker retained: %v", err)
	}
	// Retiring the attempt also frees the helper's record of it, or the next
	// commit is refused as unfinished (CEMPKOWO-HALA loop, 2026-09-25).
	if len(c.released) != 1 || c.released[0] != in.ID {
		t.Fatalf("native writer record not released for %s: %v", in.ID, c.released)
	}
}

func TestCompletedDirectoryMoveIntentAcceptsDescendantRenameSources(t *testing.T) {
	s, _, _, wc := transactionFixture(t)
	in := &commitIntent{
		Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: wc,
		Phase: "done", FirstRevision: 44, Revision: 44,
		Paths: []string{"new/place/file.pdf", "old/place"},
		Items: []intentItem{{Rel: "new/place/file.pdf", OldRel: "old/place/file.pdf", Op: watcher.Renamed}},
	}
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.readIntent(wc)
	if err != nil || got == nil || got.Revision != 44 {
		t.Fatalf("completed move intent rejected: intent=%+v err=%v", got, err)
	}
}

func TestConfirmedDirectoryDeletionClosesPendingActivity(t *testing.T) {
	s, _, journal, wc := transactionFixture(t)
	if err := journal.Record(activity.Entry{RepoID: s.repoID, Path: "gone", Kind: activity.Deleted, Stage: activity.Pending}); err != nil {
		t.Fatal(err)
	}
	in := &commitIntent{
		Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: wc,
		Phase: "confirmed", FirstRevision: 46, Revision: 46, Paths: []string{"gone"},
		Items: []intentItem{{Rel: "gone", Op: watcher.Deleted, IsDir: true}},
	}
	if err := s.finishIntent(t.Context(), wc, in); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, entry := range journal.List() {
		if entry.Path == "gone" {
			found = true
			if entry.Stage != activity.Published || entry.Revision != 46 {
				t.Fatalf("directory activity not terminal: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("directory activity disappeared")
	}
}

func TestDoneIntentRepairsLegacyPendingDirectoryActivity(t *testing.T) {
	s, _, journal, wc := transactionFixture(t)
	if err := journal.Record(activity.Entry{RepoID: s.repoID, Path: "gone", Kind: activity.Deleted, Stage: activity.Pending}); err != nil {
		t.Fatal(err)
	}
	original := journal.List()[0]
	in := &commitIntent{
		Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: wc,
		Phase: "done", FirstRevision: 46, Revision: 46, Paths: []string{"gone"},
		Items: []intentItem{{Rel: "gone", Op: watcher.Deleted, IsDir: true}},
	}
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	// Establish the old row/receipt order explicitly. Filesystem mtime can be
	// coarser than time.Now; uncertain same-quantum ordering must stay deferred.
	completed := original.UpdatedAt.Add(time.Second)
	if err := os.Chtimes(transactionPath(wc), completed, completed); err != nil {
		t.Fatal(err)
	}
	if found, err := s.recoverCommit(t.Context(), wc); err != nil || found {
		t.Fatalf("done recovery: found=%v err=%v", found, err)
	}
	for _, entry := range journal.List() {
		if entry.Path == "gone" && entry.Stage == activity.Published && entry.Revision == 46 {
			if !entry.UpdatedAt.Equal(original.UpdatedAt) || !entry.DetectedAt.Equal(original.DetectedAt) {
				t.Fatal("repair made an old operation look new", entry)
			}
			return
		}
	}
	t.Fatal("legacy pending directory activity was not repaired")
}

func TestDoneIntentDoesNotResurrectRetainedHistoryAcrossPollAndRestart(t *testing.T) {
	s, _, journal, wc := transactionFixture(t)
	path := filepath.Join(t.TempDir(), "recent.json")
	var err error
	journal.Journal, err = activity.Open(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	receipt := activity.Entry{RepoID: s.repoID, Path: "old-dir", Kind: activity.Deleted, Stage: activity.Published, Revision: 8}
	if err := journal.Record(receipt); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record(activity.Entry{RepoID: "another-repo", Path: "new.txt", Kind: activity.Added, Stage: activity.Published, Revision: 60}); err != nil {
		t.Fatal(err)
	}
	in := &commitIntent{Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: wc,
		Phase: "done", FirstRevision: 8, Revision: 8, Paths: []string{"old-dir"}, Items: []intentItem{{Rel: "old-dir", Op: watcher.Deleted, IsDir: true}}}
	if err := s.writeIntent(wc, in); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		journal.Journal, err = activity.Open(path, 1)
		if err != nil {
			t.Fatal(err)
		}
		if found, err := s.recoverCommit(t.Context(), wc); found || err != nil {
			t.Fatal(found, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(original) {
			t.Fatal("completed receipt resurrected pruned history or evicted a new event", err)
		}
	}
}

func TestDoneIntentDoesNotReplaceLaterActivityOrCurrentStaging(t *testing.T) {
	for _, stage := range []activity.Stage{activity.Published, activity.Received, activity.Reconciled, activity.Pending} {
		t.Run(string(stage), func(t *testing.T) {
			s, _, journal, wc := transactionFixture(t)
			in := &commitIntent{Schema: transactionSchema, ID: uuid.NewString(), RepoURL: s.RepoURL, RepoID: s.repoID, WC: wc,
				Phase: "done", FirstRevision: 8, Revision: 8, Paths: []string{"a.txt"}, Items: []intentItem{{Rel: "a.txt", Op: watcher.Modified}}}
			if err := s.writeIntent(wc, in); err != nil {
				t.Fatal(err)
			}
			old := time.Date(2026, 9, 28, 12, 17, 40, 0, time.UTC)
			if err := os.Chtimes(transactionPath(wc), old, old); err != nil {
				t.Fatal(err)
			}
			entry := activity.Entry{RepoID: s.repoID, Path: "a.txt", Kind: activity.Modified, Stage: stage}
			if stage == activity.Published || stage == activity.Received {
				entry.Revision = 9
			}
			if err := journal.Record(entry); err != nil {
				t.Fatal(err)
			}
			before := journal.List()[0]
			if found, err := s.recoverCommit(t.Context(), wc); found || err != nil {
				t.Fatal(found, err)
			}
			after := journal.List()[0]
			if after.Stage != before.Stage || after.Revision != before.Revision || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatal("old receipt overwrote later activity/current staging", before, after)
			}
		})
	}
}

type recordOnlyCompletedJournal struct{ calls int }

func (j *recordOnlyCompletedJournal) Record(activity.Entry) error { j.calls++; return nil }
func (j *recordOnlyCompletedJournal) Forget(string, string) error { return nil }

func TestDoneIntentHasNoUnsafeFallbackOrCrossRepositoryRepair(t *testing.T) {
	s, _, journal, _ := transactionFixture(t)
	entry := activity.Entry{RepoID: s.repoID, Path: "a.txt", Kind: activity.Modified, Stage: activity.Pending}
	if err := journal.Record(entry); err != nil {
		t.Fatal(err)
	}
	before := journal.List()[0]
	in := &commitIntent{RepoID: s.repoID, RepoURL: s.RepoURL, Phase: "done", Revision: 8,
		Items: []intentItem{{Rel: "a.txt", Op: watcher.Modified}}, loadedModifiedAt: before.UpdatedAt.Add(time.Hour)}
	// A queued newer operation is protected even if filesystem time is ahead.
	if err := s.recordIntentPublishedActivity(in); err != nil {
		t.Fatal(err)
	}
	if got := journal.List()[0]; got.Stage != activity.Pending {
		t.Fatal("current staging completed by old receipt", got)
	}
	delete(s.staging, "a.txt")
	in.RepoURL = "file:///former-repository"
	if err := s.recordIntentPublishedActivity(in); err != nil {
		t.Fatal(err)
	}
	if got := journal.List()[0]; got.Stage != activity.Pending {
		t.Fatal("foreign receipt changed current repository", got)
	}
	in.RepoURL = s.RepoURL
	sink := &recordOnlyCompletedJournal{}
	s.Activity = sink
	if err := s.recordIntentPublishedActivity(in); err != nil || sink.calls != 0 {
		t.Fatal("custom journal fell back to destructive replay", sink.calls, err)
	}
}

func TestOrphanPendingActivityReconcilesOnlyCleanUnstagedPaths(t *testing.T) {
	s, client, journal, wc := transactionFixture(t)
	if err := os.Mkdir(filepath.Join(wc, "clean-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	client.statuses["clean-dir"] = "normal"
	delete(s.staging, "a.txt")
	for _, rel := range []string{"clean-dir", "gone-dir"} {
		if err := journal.Record(activity.Entry{RepoID: s.repoID, Path: rel, Kind: activity.Deleted, Stage: activity.Pending}); err != nil {
			t.Fatal(err)
		}
	}
	s.staging["still-staged"] = &stageItem{Rel: "still-staged", Op: watcher.Deleted}
	if err := journal.Record(activity.Entry{RepoID: s.repoID, Path: "still-staged", Kind: activity.Deleted, Stage: activity.Pending}); err != nil {
		t.Fatal(err)
	}
	s.repairOrphanPendingActivity(t.Context(), wc)
	states := map[string]activity.Stage{}
	for _, entry := range journal.List() {
		states[entry.Path] = entry.Stage
	}
	if states["clean-dir"] != activity.Reconciled || states["gone-dir"] != activity.Reconciled || states["still-staged"] != activity.Pending {
		t.Fatalf("activity stages = %#v", states)
	}
}
