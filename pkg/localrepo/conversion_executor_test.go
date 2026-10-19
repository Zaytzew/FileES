package localrepo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This adapter exercises the executor contract only. Native CFAPI registration,
// the supervisor fence and local-data preservation need their own E2E adapter.
type conversionEffectStub struct {
	prepare      ConversionPreparation
	calls        []string
	bad          string
	wait         string
	releases     int
	interrupt    func(string)
	rootRestored bool
	point        ConversionPoint
	rollbackID   string
}

func (s *conversionEffectStub) call(name string) error {
	s.calls = append(s.calls, name)
	if s.interrupt != nil {
		s.interrupt(name)
	}
	if s.wait == name {
		return ErrConversionWaiting
	}
	if s.bad == name {
		return errors.New("fixture failure at " + name)
	}
	return nil
}
func (s *conversionEffectStub) Acquire(_ context.Context, _ ConversionRecord) (func(), error) {
	if err := s.call("acquire"); err != nil {
		return nil, err
	}
	return func() { s.releases++ }, nil
}
func (s *conversionEffectStub) Revalidate(context.Context, ConversionRecord) error {
	return s.call("binding")
}
func (s *conversionEffectStub) Prepare(context.Context, ConversionRecord) (ConversionPreparation, error) {
	return s.prepare, s.call("prepare")
}
func (s *conversionEffectStub) Retain(context.Context, ConversionRecord) error {
	return s.call("retain")
}
func (s *conversionEffectStub) Point(context.Context, ConversionRecord) (ConversionPoint, error) {
	return s.point, s.call("point")
}
func (s *conversionEffectStub) Preserve(context.Context, ConversionRecord) error {
	return s.call("preserve")
}
func (s *conversionEffectStub) Verify(context.Context, ConversionRecord) error {
	return s.call("verify")
}
func (s *conversionEffectStub) Cleanup(context.Context, ConversionRecord) error {
	return s.call("cleanup")
}
func (s *conversionEffectStub) Rollback(_ context.Context, r ConversionRecord) (string, error) {
	if err := s.call("rollback"); err != nil {
		return "", err
	}
	s.rootRestored = true
	if s.rollbackID != "" {
		return s.rollbackID, nil
	}
	return r.SourceFileID, nil
}

func executorFixture(t *testing.T) (*ConversionExecutor, *conversionEffectStub) {
	t.Helper()
	j, _ := conversionFixture(t)
	s := &conversionEffectStub{point: ConversionPoint{"point-file-id", "fixture-provider"}, prepare: ConversionPreparation{
		SourceFileID: "source-file-id", Revision: 0, PlanPath: filepath.Join(t.TempDir(), "plan"), PlanSHA256: strings.Repeat("ab", 32), LocalObjects: 3}}
	return &ConversionExecutor{Journal: j, Effects: s}, s
}

func TestConversionExecutorCannotDeferPreparedRevalidation(t *testing.T) {
	e, s := executorFixture(t)
	conversionPrepared(t, e.Journal)
	s.wait = "binding"
	r, err := e.Run(t.Context())
	if err == nil || errors.Is(err, ErrConversionWaiting) || r.Blocked == "" || r.Phase != ConversionRetaining {
		t.Fatalf("prepared operation lost its execution fence: %+v %v", r, err)
	}
}

func reopenConversionExecutor(t *testing.T, e *ConversionExecutor) {
	t.Helper()
	j, err := OpenConversionJournal(e.Journal.path)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal = j
}

func TestConversionExecutorRestartsAtEveryEffectAndReceipt(t *testing.T) {
	for _, boundary := range []string{
		"waiting/effect", "retaining/receipt", "retaining/effect", "retained/receipt", "creating/receipt", "creating/effect",
		"preserving/receipt", "preserving/effect", "verifying/receipt", "verifying/effect", "cleanup/receipt", "cleanup/effect", "complete/receipt",
	} {
		t.Run(boundary, func(t *testing.T) {
			e, s := executorFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			hit := false
			e.Progress = func(p ConversionProgress) {
				if string(p.Phase)+"/"+p.Boundary == boundary {
					hit = true
					cancel()
				}
			}
			r, err := e.Run(ctx)
			if !hit || (!errors.Is(err, context.Canceled) && r.Phase != ConversionComplete) {
				t.Fatalf("not interrupted: %+v %v", r, err)
			}
			if r.Blocked != "" || s.releases != 1 {
				t.Fatalf("cancellation became failure or leaked fence: %+v releases=%d", r, s.releases)
			}
			reopenConversionExecutor(t, e)
			e.Progress = nil
			r, err = e.Run(t.Context())
			if err != nil || r.Phase != ConversionComplete {
				t.Fatalf("resume=%+v %v", r, err)
			}
			calls := len(s.calls)
			if _, err := e.Run(t.Context()); err != nil || len(s.calls) != calls {
				t.Fatal("terminal operation replayed")
			}
		})
	}
}

func TestConversionExecutorWaitingFailureAndExplicitRetry(t *testing.T) {
	for _, stage := range []string{"acquire", "binding", "prepare", "retain", "point", "preserve", "verify", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			e, s := executorFixture(t)
			s.bad = stage
			r, err := e.Run(t.Context())
			if err == nil || r.Blocked == "" {
				t.Fatalf("failure not retained: %+v %v", r, err)
			}
			reopenConversionExecutor(t, e)
			n := len(s.calls)
			if _, err := e.Run(t.Context()); err == nil || len(s.calls) != n {
				t.Fatal("restart retried known failure")
			}
			s.bad = ""
			if _, err := e.Journal.Retry(e.Journal.Get()); err != nil {
				t.Fatal(err)
			}
			if r, err := e.Run(t.Context()); err != nil || r.Phase != ConversionComplete {
				t.Fatalf("explicit retry=%+v %v", r, err)
			}
		})
	}
	for _, stage := range []string{"acquire", "binding", "prepare"} {
		e, s := executorFixture(t)
		s.wait = stage
		r, err := e.Run(t.Context())
		if !errors.Is(err, ErrConversionWaiting) || r.Phase != ConversionWaiting || r.Blocked != "" {
			t.Fatalf("waiting became error: %+v %v", r, err)
		}
	}
	// An effect cannot use a waiting label to conceal an ambiguous mutation.
	e, s := executorFixture(t)
	s.wait = "retain"
	if r, err := e.Run(t.Context()); err == nil || r.Blocked == "" || errors.Is(err, ErrConversionWaiting) {
		t.Fatalf("post-effect wait hidden: %+v %v", r, err)
	}
}

func TestConversionExecutorRollbackDirectionSurvivesInterruption(t *testing.T) {
	for _, boundary := range []string{"rolling_back/receipt", "rolling_back/effect"} {
		t.Run(boundary, func(t *testing.T) {
			e, s := executorFixture(t)
			s.bad = "preserve"
			if _, err := e.Run(t.Context()); err == nil {
				t.Fatal("missing fixture failure")
			}
			s.bad = ""
			ctx, cancel := context.WithCancel(t.Context())
			e.Progress = func(p ConversionProgress) {
				if string(p.Phase)+"/"+p.Boundary == boundary {
					cancel()
				}
			}
			if r, err := e.Rollback(ctx); !errors.Is(err, context.Canceled) || r.Phase != ConversionRollingBack {
				t.Fatalf("rollback intent lost: %+v %v", r, err)
			}
			reopenConversionExecutor(t, e)
			e.Progress = nil
			s.calls = nil
			r, err := e.Run(t.Context())
			if err != nil || r.Phase != ConversionRolledBack || !s.rootRestored {
				t.Fatalf("recovery=%+v %v", r, err)
			}
			for _, name := range s.calls {
				if name != "acquire" && name != "binding" && name != "rollback" {
					t.Fatalf("forward effect during recovery: %s", name)
				}
			}
		})
	}
}

func TestConversionExecutorRechecksBeforeCleanupAndRejectsParallelRun(t *testing.T) {
	e, s := executorFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	e.Progress = func(p ConversionProgress) {
		if p.Phase == ConversionCleanup && p.Boundary == "receipt" {
			cancel()
		}
	}
	if _, err := e.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	reopenConversionExecutor(t, e)
	e.Progress = nil
	s.calls = nil
	s.bad = "verify"
	r, err := e.Run(t.Context())
	if err == nil || r.Phase != ConversionCleanup || r.Blocked == "" {
		t.Fatalf("late write not blocking: %+v %v", r, err)
	}
	for _, name := range s.calls {
		if name == "cleanup" {
			t.Fatal("cleanup before fresh verification")
		}
	}

	e, s = executorFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.interrupt = func(name string) {
		if name == "prepare" {
			once.Do(func() { close(started); <-release })
		}
	}
	done := make(chan error, 1)
	go func() { _, err := e.Run(t.Context()); done <- err }()
	<-started
	if _, err := e.Run(t.Context()); !errors.Is(err, ErrConversionExecuting) {
		t.Fatalf("parallel execution: %v", err)
	}
	if _, err := e.Rollback(t.Context()); !errors.Is(err, ErrConversionExecuting) {
		t.Fatalf("parallel rollback: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConversionExecutorRejectsInvalidEffectReceipts(t *testing.T) {
	e, s := executorFixture(t)
	s.prepare.SourceFileID = ""
	if r, err := e.Run(t.Context()); err == nil || r.Blocked == "" {
		t.Fatalf("invalid preparation replayable: %+v %v", r, err)
	}
	if _, err := (&ConversionExecutor{}).Run(t.Context()); err == nil {
		t.Fatal("unconfigured executor accepted")
	}
	e, s = executorFixture(t)
	s.point = ConversionPoint{FileID: s.prepare.SourceFileID, ProviderID: "provider"}
	if r, err := e.Run(t.Context()); err == nil || r.Blocked == "" || r.Phase != ConversionCreating {
		t.Fatalf("invalid point receipt: %+v %v", r, err)
	}
	e, s = executorFixture(t)
	s.bad = "preserve"
	if _, err := e.Run(t.Context()); err == nil {
		t.Fatal("missing failure")
	}
	s.bad = ""
	s.rollbackID = "foreign-root"
	if r, err := e.Rollback(t.Context()); err == nil || r.Blocked == "" || r.Phase != ConversionRollingBack {
		t.Fatalf("false rollback completion: %+v %v", r, err)
	}
	e, _ = executorFixture(t)
	if _, err := e.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Rollback(t.Context()); err == nil {
		t.Fatal("completed conversion falsely reported rollback")
	}
}
