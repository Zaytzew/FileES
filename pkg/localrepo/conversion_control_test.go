package localrepo

import (
	"errors"
	"testing"
)

func TestConversionControlIsDurableScopedAndExcludesExecutor(t *testing.T) {
	j, _ := conversionFixture(t)
	waiting := j.Get()
	j.runMu.Lock()
	if _, err := j.Control(waiting, "rollback"); !errors.Is(err, ErrConversionExecuting) {
		t.Fatal("control entered running executor", err)
	}
	j.runMu.Unlock()
	if j.Get() != waiting {
		t.Fatal("running control mutated receipt")
	}
	canceled, err := j.Control(waiting, "rollback")
	if err != nil || canceled.Phase != ConversionCanceled {
		t.Fatal(canceled, err)
	}
	reopened, err := OpenConversionJournal(j.path)
	if err != nil || reopened.Get() != canceled {
		t.Fatal("direction not durable", err)
	}
	if _, err := j.Control(waiting, "retry"); err == nil {
		t.Fatal("stale receipt accepted")
	}
}

func TestConversionControlNeverRollsBackCleanup(t *testing.T) {
	j, _ := conversionFixture(t)
	r := conversionPrepared(t, j)
	r, _ = j.MarkRetained(r)
	r, _ = j.BeginPoint(r)
	r, _ = j.MarkPointReady(r, "point", "FileES")
	r, _ = j.MarkPreserved(r)
	r, _ = j.MarkVerified(r)
	r, _ = j.Block(r, errors.New("open handle"))
	if _, err := j.Control(r, "rollback"); err == nil || j.Get() != r {
		t.Fatal("cleanup rollback admitted", err)
	}
	next, err := j.Control(r, "retry")
	if err != nil || next.Phase != ConversionCleanup || next.Blocked != "" {
		t.Fatal(next, err)
	}
}
