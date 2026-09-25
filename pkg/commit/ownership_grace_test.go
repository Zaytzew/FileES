package commit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"filees/pkg/errcat"
	"filees/pkg/errmap"
	"filees/pkg/talk"
)

// Owner's production, 2026-09-25: right after a start and after an update the
// journal showed "LOCK-2106 · wymagane działanie użytkownika" while the next
// poll succeeded. Unknown ownership is reported only when it lasts.
func TestOwnedAccessReportsUnknownOwnershipOnlyWhenItLasts(t *testing.T) {
	var journal bytes.Buffer
	var result error
	s := &Service{RealmID: "owner", OwnerRealmID: "owner", Logger: talk.With("grace"), ErrSink: errmap.NewSink(&journal, "commit:test"),
		AutoUnlockOwned: func(context.Context, string, string) error { return result }}

	result = errcat.New(errcat.KeyPathOwnerUnavailable, nil, nil)
	s.ReconcileOwnedAccess(t.Context(), "wc")
	s.ReconcileOwnedAccess(t.Context(), "wc")
	if journal.Len() != 0 {
		t.Fatalf("a momentary unknown reached the journal: %s", journal.String())
	}

	s.mu.Lock()
	s.ownershipUnknownAt = time.Now().Add(-ownershipUnknownGrace - time.Second)
	s.mu.Unlock()
	s.ReconcileOwnedAccess(t.Context(), "wc")
	if !strings.Contains(journal.String(), "LOCK-2106") {
		t.Fatalf("a lasting unknown was not reported: %q", journal.String())
	}

	// Success ends the streak: the next unknown starts a new grace.
	journal.Reset()
	result = nil
	s.ReconcileOwnedAccess(t.Context(), "wc")
	result = errcat.New(errcat.KeyPathOwnerUnavailable, nil, nil)
	s.ReconcileOwnedAccess(t.Context(), "wc")
	if journal.Len() != 0 {
		t.Fatalf("grace did not restart after a success: %s", journal.String())
	}

	// Any other failure is reported at once.
	result = errors.New("backend cannot list needs-lock paths")
	s.ReconcileOwnedAccess(t.Context(), "wc")
	if journal.Len() == 0 {
		t.Fatal("a real failure waited for the ownership grace")
	}
}
