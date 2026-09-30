package commit

import (
	"context"
	"errors"
	"testing"
)

func TestStorageGateKeepsBatchAndDoesNotRetryUncertainCommit(t *testing.T) {
	s, c, _, wc := transactionFixture(t)
	wait := errors.New("waiting for server storage")
	calls := 0
	s.CheckPublication = func(context.Context) error { calls++; return wait }
	before := len(s.staging)
	for range 3 {
		if err := s.tryCommit(t.Context(), wc); !errors.Is(err, wait) {
			t.Fatal(err)
		}
	}
	if calls != 3 || c.mutations != 0 || len(s.staging) != before {
		t.Fatal("gate lost batch or published", calls, c.mutations, len(s.staging))
	}
	s.CheckPublication = nil
	c.noEffect = true
	if err := s.tryCommit(t.Context(), wc); err == nil {
		t.Fatal("expected uncertain commit")
	}
	s.CheckPublication = func(context.Context) error { t.Fatal("gate ran before recovery"); return wait }
	_, _ = s.recoverCommit(t.Context(), wc)
	if c.mutations != 1 {
		t.Fatal("recovery retried mutation", c.mutations)
	}
}
