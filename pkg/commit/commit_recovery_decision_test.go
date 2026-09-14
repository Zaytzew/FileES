package commit

import (
	"os"
	"path/filepath"
	"testing"

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
	s, _, _, wc := transactionFixture(t)
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
