package anchorqueue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

func queueFixture(t *testing.T) (*Store, contract.RepoAnchorQueuePayload, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "queue.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := contract.RepoAnchorQueuePayload{IntentID: uuid.NewString(), Files: 3,
		RepoAnchorBatchPayload: contract.RepoAnchorBatchPayload{Fingerprint: strings.Repeat("ab", 32),
			RepoAnchorPlanPayload: contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: ".", Action: "release"}}}
	if _, err := s.Enqueue(p, "operation", "file:///repo", root); err != nil {
		t.Fatal(err)
	}
	return s, p, path
}

func TestQueuedRestartBusyThenExactlyOneEffectAndDurableResult(t *testing.T) {
	s, p, path := queueFixture(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	busy := errors.New("pre-effect busy")
	var r Runner
	effects, calls := 0, 0
	execute := func(context.Context, Intent) (contract.RepoAnchorBatchResult, error) {
		calls++
		if calls == 1 {
			return contract.RepoAnchorBatchResult{}, busy
		}
		effects++
		return contract.RepoAnchorBatchResult{State: "attached", Completed: 3}, nil
	}
	ready := func(Intent) (bool, error) { return true, nil }
	if err := r.Tick(t.Context(), s, busy, ready, execute); err != nil || s.List()[0].Result.State != "queued" || effects != 0 {
		t.Fatal(err, s.List(), effects)
	}
	if err := r.Tick(t.Context(), s, busy, ready, execute); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := r.Tick(t.Context(), s, busy, ready, execute); err != nil {
			t.Fatal(err)
		}
	}
	x, err := s.Enqueue(p, "operation", "file:///repo", filepath.Dir(path))
	if err != nil || x.Result.State != "attached" || x.Result.Completed != 3 || effects != 1 || calls != 2 {
		t.Fatal(x, err, effects, calls)
	}
	p.Path = "another"
	if _, err := s.Enqueue(p, "operation", "file:///repo", filepath.Dir(path)); !errors.Is(err, ErrConflict) {
		t.Fatal("id expanded selection", err)
	}
}

func TestRunningRestartStopsWithoutReplayingBatch(t *testing.T) {
	s, p, path := queueFixture(t)
	if err := s.Set(p.IntentID, "queued", contract.RepoAnchorBatchResult{State: "running"}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	x := s.List()[0]
	if x.Result.State != "failed" || x.Result.FailureKey != "head.release_queue_interrupted" {
		t.Fatal(x)
	}
	var r Runner
	if err := r.Tick(t.Context(), s, errors.New("busy"), func(Intent) (bool, error) { t.Fatal("running restart admitted"); return true, nil }, func(context.Context, Intent) (contract.RepoAnchorBatchResult, error) {
		t.Fatal("replayed")
		return contract.RepoAnchorBatchResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	p.IntentID = uuid.NewString()
	if _, err := s.Enqueue(p, "operation", "file:///repo", filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if !s.List()[0].Result.Superseded {
		t.Fatal("fresh confirmation retained stale alarm")
	}
	if s.List()[1].Result.State != "queued" {
		t.Fatal("manual reconfirmation not queued")
	}
}

func TestPartialAndUnknownOutcomeNeverRetry(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "unknown"}[unknown], func(t *testing.T) {
			s, _, path := queueFixture(t)
			var r Runner
			effects := 0
			execute := func(context.Context, Intent) (contract.RepoAnchorBatchResult, error) {
				effects++
				if unknown {
					return contract.RepoAnchorBatchResult{}, errors.New("lost effect receipt")
				}
				return contract.RepoAnchorBatchResult{State: "partial", Completed: 1, FailureCode: "HEAD-2018", FailureKey: "head.folder_busy"}, nil
			}
			_ = r.Tick(t.Context(), s, errors.New("busy"), func(Intent) (bool, error) { return true, nil }, execute)
			if s.List()[0].Result.State != "failed" {
				t.Fatal(s.List())
			}
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Tick(t.Context(), s, errors.New("busy"), func(Intent) (bool, error) { return true, nil }, execute); err != nil || effects != 1 {
				t.Fatal(err, effects)
			}
		})
	}
}

func TestQueuePersistenceFailurePreventsEffectAndFurtherAdmission(t *testing.T) {
	s, _, path := queueFixture(t)
	// Make atomic replacement fail without changing the retained queue file.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "occupied"), []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	var r Runner
	execute := func(context.Context, Intent) (contract.RepoAnchorBatchResult, error) {
		t.Fatal("effect before durable start")
		return contract.RepoAnchorBatchResult{}, nil
	}
	if err := r.Tick(t.Context(), s, errors.New("busy"), func(Intent) (bool, error) { return true, nil }, execute); err == nil {
		t.Fatal("write failure ignored")
	}
	if s.List()[0].Result.State != "queued" {
		t.Fatal(s.List())
	}
	if err := r.Tick(t.Context(), s, errors.New("busy"), func(Intent) (bool, error) { return true, nil }, execute); !errors.Is(err, ErrPoisoned) {
		t.Fatal(err)
	}
}

func TestQueueBoundAndNoEffectBeforeReadiness(t *testing.T) {
	s, p, path := queueFixture(t)
	for range maxPending - 1 {
		p.IntentID = uuid.NewString()
		if _, err := s.Enqueue(p, "operation", "file:///repo", filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
	}
	p.IntentID = uuid.NewString()
	if _, err := s.Enqueue(p, "operation", "file:///repo", filepath.Dir(path)); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	var r Runner
	if err := r.Tick(t.Context(), s, errors.New("busy"), func(Intent) (bool, error) { return false, nil }, func(context.Context, Intent) (contract.RepoAnchorBatchResult, error) {
		t.Fatal("not ready")
		return contract.RepoAnchorBatchResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Tick(ctx, s, errors.New("busy"), func(Intent) (bool, error) { t.Fatal("canceled"); return true, nil }, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrentTicksCannotReplayRunningIntention(t *testing.T) {
	s, _, _ := queueFixture(t)
	var r Runner
	var effects atomic.Int32
	entered, finish, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	execute := func(context.Context, Intent) (contract.RepoAnchorBatchResult, error) {
		effects.Add(1)
		close(entered)
		<-finish
		return contract.RepoAnchorBatchResult{State: "attached", Completed: 3}, nil
	}
	busy := errors.New("busy")
	ready := func(Intent) (bool, error) { return true, nil }
	go func() { done <- r.Tick(t.Context(), s, busy, ready, execute) }()
	<-entered
	for range 8 {
		if err := r.Tick(t.Context(), s, busy, ready, execute); err != nil {
			t.Fatal(err)
		}
	}
	close(finish)
	if err := <-done; err != nil || effects.Load() != 1 {
		t.Fatal(err, effects.Load())
	}
}

func TestLostDurableResultStopsFurtherEffectsAndRestartReplay(t *testing.T) {
	s, _, path := queueFixture(t)
	var r Runner
	effects := 0
	execute := func(context.Context, Intent) (contract.RepoAnchorBatchResult, error) {
		effects++
		// Retain the durable running record while making its replacement fail.
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return contract.RepoAnchorBatchResult{State: "attached", Completed: 3}, nil
	}
	busy := errors.New("busy")
	ready := func(Intent) (bool, error) { return true, nil }
	if err := r.Tick(t.Context(), s, busy, ready, execute); err == nil || s.Fault() == "" {
		t.Fatal("lost result accepted", err)
	}
	if err := r.Tick(t.Context(), s, busy, ready, execute); err != nil {
		t.Fatal(err)
	}
	if effects != 1 {
		t.Fatal(effects)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	} // empty fixture directory only
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.List()[0].Result.State != "failed" {
		t.Fatal(s.List())
	}
	if err := r.Tick(t.Context(), s, busy, ready, execute); err != nil || effects != 1 {
		t.Fatal(err, effects)
	}
}
