package localrepo

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConversionLifecycleProjectsOnlyProvenTerminalOutcome(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "rollback"}[rollback], func(t *testing.T) {
			s, j, r := lifecycleConversionFixture(t)
			queued, err := s.QueueConversion(r, j.Get())
			if err != nil {
				t.Fatal(err)
			}
			r, err = s.StartConversion(queued)
			if err != nil {
				t.Fatal(err)
			}
			_, effects := executorFixture(t)
			e := &ConversionExecutor{Journal: j, Effects: effects}
			if rollback {
				conversionPrepared(t, j)
			}
			var outcome ConversionRecord
			if rollback {
				outcome, err = e.Rollback(context.Background())
			} else {
				outcome, err = e.Run(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			finished, err := s.FinishConversion(r, outcome)
			if err != nil {
				t.Fatal(err)
			}
			if finished.State != StateAttached || finished.Anchor != !rollback || finished.Sparse != !rollback || finished.ConversionID != "" || finished.PendingLocalPath != "" || finished.RelocationQueued || finished.LocalPath != queued.LocalPath {
				t.Fatalf("invalid terminal projection %+v", finished)
			}
			if _, err := s.FinishConversion(r, outcome); err == nil {
				t.Fatal("stale terminal receipt replayed")
			}
		})
	}
}

func lifecycleConversionFixture(t *testing.T) (*Store, *ConversionJournal, Record) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.EnsureConfiguredAttached("lab", "repo", "svn+ssh://fixture/repo", "rw", filepath.Join(t.TempDir(), "WC"), "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	j, err := CreateConversionJournal(filepath.Join(t.TempDir(), "conversion.json"), r)
	if err != nil {
		t.Fatal(err)
	}
	return s, j, r
}

func TestConversionLifecycleReservesBothRootsAndCannotUseMoveCleanup(t *testing.T) {
	s, j, r := lifecycleConversionFixture(t)
	r, err := s.QueueConversion(r, j.Get())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateAttached || r.ConversionID != j.Get().ID || r.PendingLocalPath != j.Get().BackupPath {
		t.Fatal(r)
	}
	for _, target := range []string{r.LocalPath, r.PendingLocalPath, filepath.Join(r.PendingLocalPath, "nested")} {
		if _, err := s.BeginCreate("lab", "Other", target); err == nil {
			t.Fatalf("stole reserved path %s", target)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := s.BeginCreate("lab", "Other", strings.ToLower(r.PendingLocalPath)); err == nil {
			t.Fatal("case alias stole backup path")
		}
	}
	if _, err := s.BeginDetach("lab", "repo", false); err == nil {
		t.Fatal("ordinary detach superseded conversion")
	}
	if _, err := s.QueueRelocation("lab", "repo", r.PendingLocalPath); err == nil {
		t.Fatal("ordinary move adopted conversion reservation")
	}
	if _, err := s.StartQueuedRelocation(r); err == nil {
		t.Fatal("ordinary relocation executed a conversion")
	}
	if err := s.CheckAnchorRetirement(); err == nil {
		t.Fatal("nocfapi update passed pending conversion")
	}
	r, err = s.StartConversion(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteRelocation(r.OperationID); err == nil {
		t.Fatal("ordinary move completed conversion")
	}
	if _, err := s.DeferQueuedRelocation(r); err == nil {
		t.Fatal("ordinary deferral reopened converted path")
	}
	s, err = Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := s.Get(r.OperationID)
	if !ok || persisted != r {
		t.Fatal("lost conversion reservation")
	}
	if _, err := s.DeferConversion(r, j.Get()); err != nil {
		t.Fatal(err)
	}
}

func TestConversionLifecycleRejectsChangedAndProgressedJournal(t *testing.T) {
	s, j, r := lifecycleConversionFixture(t)
	wrong := j.Get()
	wrong.RepoURL = "svn+ssh://other/repo"
	if _, err := s.QueueConversion(r, wrong); err == nil {
		t.Fatal("wrong authority accepted")
	}
	r, err := s.QueueConversion(r, j.Get())
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.StartConversion(r)
	if err != nil {
		t.Fatal(err)
	}
	prepared := conversionPrepared(t, j)
	if _, err := s.DeferConversion(r, prepared); err == nil {
		t.Fatal("writer reopened after preparation")
	}
	if _, err := s.FinishConversion(r, prepared); err == nil {
		t.Fatal("nonterminal journal completed lifecycle")
	}
	if _, err := s.RecordConversionError(r.OperationID, r.ConversionID, errors.New("fixture failure")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishConversion(r, prepared); err == nil {
		t.Fatal("stale lifecycle receipt accepted")
	}
}

func TestConversionLifecycleTerminalOutcomesAndRemoteWithdrawal(t *testing.T) {
	s, j, r := lifecycleConversionFixture(t)
	r, err := s.QueueConversion(r, j.Get())
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := j.Cancel(j.Get())
	if err != nil {
		t.Fatal(err)
	}
	finished, err := s.FinishConversion(r, canceled)
	if err != nil || finished.ConversionID != "" || finished.Anchor {
		t.Fatalf("cancel=%+v %v", finished, err)
	}
	if err := s.CheckAnchorRetirement(); err != nil {
		t.Fatal(err)
	}

	s, j, r = lifecycleConversionFixture(t)
	r, err = s.QueueConversion(r, j.Get())
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.StartConversion(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveRemoteDeletion("lab", "repo"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(r.OperationID)
	if !r.RemoteDeletionObserved || r.PreservedAlternatePath != j.Get().BackupPath || r.ConversionID != j.Get().ID {
		t.Fatal("withdrawal lost conversion custody", r)
	}
	if err := s.BeginRemoteCleanup(r.OperationID, "unknown"); err == nil {
		t.Fatal("normal remote cleanup stripped conversion metadata")
	}
	if _, err := s.BeginCreate("lab", "Other", r.PreservedAlternatePath); err == nil {
		t.Fatal("withdrawn conversion lost backup reservation")
	}
}
