package commit

import (
	"bytes"
	"context"
	"errors"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestOwnCancellationReporting(t *testing.T) {
	for _, tt := range []struct {
		name  string
		stop  bool
		err   error
		quiet bool
	}{
		{"own", true, context.Canceled, true},
		{"wrapped joined", true, fmt.Errorf("native: %w", errors.Join(context.Canceled, context.Canceled)), true},
		{"independent cancel", false, context.Canceled, false},
		{"deadline", true, context.DeadlineExceeded, false},
		{"real error during stop", true, errors.New("disk failed"), false},
		{"joined real error", true, errors.Join(context.Canceled, errors.New("disk failed")), false},
		{"text only", true, errors.New("context canceled"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.stop {
				cancel()
			}
			var log bytes.Buffer
			s := &Service{ErrSink: errmap.NewSink(&log, "test")}
			s.recordCommitFailureContext(ctx, "test", tt.err)
			if (log.Len() == 0) != tt.quiet {
				t.Fatalf("log=%s", log.String())
			}
			if tt.quiet && s.heldConsecutive != 0 {
				t.Fatal("cancellation counted as held")
			}
			log.Reset()
			s.RealmID, s.OwnerRealmID = "realm", "realm"
			s.ownershipUnknownAt = time.Now().Add(-ownershipUnknownGrace - time.Minute)
			before := s.ownershipUnknownAt
			s.AutoUnlockOwned = func(context.Context, string, string) error {
				return errcat.New(errcat.KeyPathOwnerUnavailable, nil, tt.err)
			}
			s.ReconcileOwnedAccess(ctx, t.TempDir())
			if (log.Len() == 0) != tt.quiet {
				t.Fatalf("ownership log=%s", log.String())
			}
			if s.ownershipUnknownAt != before {
				t.Fatal("cancellation changed ownership episode")
			}
		})
	}
}

func TestCancellationPresentationPreservesUncertainIntent(t *testing.T) {
	s, _, wc := conflictRecoveryFixture(t)
	before, e := os.ReadFile(transactionPath(wc))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var log bytes.Buffer
	s.ErrSink = errmap.NewSink(&log, "test")
	s.heldConsecutive = 2
	s.heldReported = true
	s.recordCommitFailureContext(ctx, "stopping", context.Canceled)
	after, e := os.ReadFile(transactionPath(wc))
	if e != nil || !bytes.Equal(before, after) || !s.intentPending(wc) || !s.heldReported || s.heldConsecutive != 2 || log.Len() != 0 {
		t.Fatal("cancellation retired recovery or invented success", e)
	}
}
