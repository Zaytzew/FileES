package localrepo

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestAnchorRetirementStates(t *testing.T) {
	for _, state := range []State{StateUnattached, StatePolicyPending, StateAttaching, StateAttached, StateRelocating, StateReconciling, StateDetaching, StateDeleting, StateError, StateAbandoned, StateDetached, StateDeleted} {
		t.Run(string(state), func(t *testing.T) {
			s := &Store{records: map[string]Record{"point": {Anchor: true, State: state}}}
			wantAllowed := state == StateDetached || state == StateDeleted
			if err := s.CheckAnchorRetirement(); (err == nil) != wantAllowed {
				t.Fatalf("check=%v, allowed=%v", err, wantAllowed)
			}
			if err := s.RetireAnchors(); (err == nil) != wantAllowed {
				t.Fatalf("retire=%v, allowed=%v", err, wantAllowed)
			}
		})
	}
	s := &Store{records: map[string]Record{"point": {Anchor: true, State: StateDetached, RemoteDeletionObserved: true}}}
	if err := s.RetireAnchors(); err == nil {
		t.Fatal("remote withdrawal with unfinished local cleanup accepted")
	}
}

func TestAnchorAttachAndRetirementAreMutuallyExclusive(t *testing.T) {
	for i := 0; i < 30; i++ {
		s, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
		if err != nil {
			t.Fatal(err)
		}
		point := filepath.Join(t.TempDir(), "point")
		start := make(chan struct{})
		var attachErr, retireErr error
		var done sync.WaitGroup
		done.Add(2)
		go func() {
			defer done.Done()
			<-start
			_, attachErr = s.BeginAnchorAttach("server", "repo", point, false)
		}()
		go func() {
			defer done.Done()
			<-start
			retireErr = s.RetireAnchors()
		}()
		close(start)
		done.Wait()
		if (attachErr == nil) == (retireErr == nil) {
			t.Fatalf("exactly one should succeed: attach=%v retire=%v", attachErr, retireErr)
		}
	}
}
