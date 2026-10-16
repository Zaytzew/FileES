package localrepo

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func relocationHistoryFixture(t *testing.T) (*Store, Record, string) {
	t.Helper()
	base := t.TempDir()
	s, err := Open(filepath.Join(base, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := s.EnsureConfiguredAttached("server", "repo", "svn+ssh://example/repo", "rw", filepath.Join(base, "live"), "Live")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "old-root")
	for _, state := range []State{StateDetached, StateDeleted, StateError, StateAbandoned} {
		old := live
		old.OperationID, old.State, old.LocalPath = uuid.NewString(), state, target
		if state == StateDetached || state == StateDeleted {
			old.DetachOperationID = uuid.NewString()
		}
		if state == StateDeleted {
			old.DeleteRepository, old.ServerDeleteCompleted, old.RecoveryPrepared, old.LocalCleanupCompleted = true, true, true, true
			old.RetainUntil = old.UpdatedAt.Format(time.RFC3339Nano)
		}
		if err := validate(old); err != nil {
			t.Fatal(err)
		}
		s.records[old.OperationID] = old
	}
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	return s, live, target
}

func TestRelocationIgnoresHistoricalAttachments(t *testing.T) {
	for _, mode := range []string{"queued", "move", "locate"} {
		t.Run(mode, func(t *testing.T) {
			s, live, target := relocationHistoryFixture(t)
			var err error
			s, err = Open(s.path)
			if err != nil {
				t.Fatal(err)
			}
			before := s.List()
			var got Record
			switch mode {
			case "queued":
				got, err = s.QueueRelocation(live.ServerID, live.RepoID, target)
			case "move":
				got, err = s.BeginRelocation(live.ServerID, live.RepoID, target, true)
			case "locate":
				got, err = s.BeginLocate(live.ServerID, live.RepoID, target)
			}
			if err != nil || got.OperationID != live.OperationID {
				t.Fatalf("selected historical record: %+v %v", got, err)
			}
			for _, old := range before {
				if old.OperationID != live.OperationID {
					now, _ := s.Get(old.OperationID)
					if !reflect.DeepEqual(now, old) {
						t.Fatal("history changed")
					}
				}
			}
			reopened, err := Open(s.path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "queued" {
				got, err = reopened.QueueRelocation(live.ServerID, live.RepoID, target)
			} else if mode == "move" {
				got, err = reopened.BeginRelocation(live.ServerID, live.RepoID, target, true)
			} else {
				got, err = reopened.BeginLocate(live.ServerID, live.RepoID, target)
			}
			if err != nil || got.OperationID != live.OperationID {
				t.Fatal("restart lost current intent", got, err)
			}
		})
	}
}

func TestRelocationDoesNotGuessBetweenLiveAttachments(t *testing.T) {
	s, live, _ := relocationHistoryFixture(t)
	other := live
	other.OperationID, other.LocalPath = uuid.NewString(), filepath.Join(t.TempDir(), "other")
	s.records[other.OperationID] = other
	before := s.List()
	if _, err := s.QueueRelocation(live.ServerID, live.RepoID, filepath.Join(t.TempDir(), "target")); err == nil {
		t.Fatal("ambiguous active attachment accepted")
	}
	if !reflect.DeepEqual(before, s.List()) {
		t.Fatal("ambiguous lookup changed records")
	}
}

func TestRelocationNeverRevivesHistoricalOnlyRepository(t *testing.T) {
	s, live, target := relocationHistoryFixture(t)
	delete(s.records, live.OperationID)
	before := s.List()
	if _, err := s.QueueRelocation(live.ServerID, live.RepoID, target); err == nil {
		t.Fatal("history revived")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected absence error:", err)
	}
	if !reflect.DeepEqual(before, s.List()) {
		t.Fatal("history changed")
	}
}

func TestRelocationHistoryDoesNotBypassCleanupOrOtherLiveRoot(t *testing.T) {
	for _, mode := range []string{"cleanup", "other-server", "detaching", "reconciling"} {
		t.Run(mode, func(t *testing.T) {
			s, live, target := relocationHistoryFixture(t)
			switch mode {
			case "cleanup":
				for id, r := range s.records {
					if r.State == StateDetached {
						r.RemoteDeletionObserved = true
						s.records[id] = r
					}
				}
			case "other-server":
				r := live
				r.OperationID, r.ServerID, r.LocalPath = uuid.NewString(), "another-server", target
				s.records[r.OperationID] = r
			case "detaching":
				live.State = StateDetaching
				live.DetachOperationID = uuid.NewString()
				s.records[live.OperationID] = live
			case "reconciling":
				live.State = StateReconciling
				live.ReconcileOperationID = uuid.NewString()
				s.records[live.OperationID] = live
			}
			before := s.List()
			if _, err := s.QueueRelocation(live.ServerID, live.RepoID, target); err == nil {
				t.Fatal("safety boundary bypassed")
			}
			if !reflect.DeepEqual(before, s.List()) {
				t.Fatal("failed admission changed records")
			}
		})
	}
}
