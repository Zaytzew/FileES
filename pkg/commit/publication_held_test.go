package commit

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"filees/pkg/errmap"
)

// Owner's desktop, 2026-09-23: a batch held forever wrote the same entry to the
// journal every 30 s while the tray, the radar and the server indicator all
// stayed green. Held publication must be reported, and reported once.
func TestHeldPublicationIsReportedOnceAndClearedBySuccess(t *testing.T) {
	var journal bytes.Buffer
	var held []bool
	s := &Service{ErrSink: errmap.NewSink(&journal, "commit:fixture"), OnPublicationHeld: func(h bool) { held = append(held, h) }}
	lines := func() int { return bytes.Count(journal.Bytes(), []byte("\n")) }
	stuck := errors.New(`native rename a.jpg -> 23.09.2026/a.jpg has unsupported source status "none"; publication held`)

	// One failed cycle is tolerated: it is often a working copy locked for a moment.
	s.recordCommitFailure("commit attempt failed", stuck)
	if len(held) != 0 {
		t.Fatalf("held reported after one cycle: %v", held)
	}
	if lines() != 1 {
		t.Fatalf("first failure journaled %d times", lines())
	}

	// The second consecutive cycle is held. The same failure retried every 30 s
	// adds nothing to the journal.
	for range 20 {
		s.recordCommitFailure("commit attempt failed", stuck)
	}
	if !reflect.DeepEqual(held, []bool{true}) {
		t.Fatalf("held notifications = %v, want exactly one true", held)
	}
	if lines() != 1 {
		t.Fatalf("the same held failure was journaled %d times", lines())
	}

	// A different failure is news and is journaled; it does not re-announce.
	s.recordCommitFailure("commit attempt failed", errors.New("native rename destination b.jpg disappeared; publication held"))
	if lines() != 2 || len(held) != 1 {
		t.Fatalf("different failure: lines=%d held=%v", lines(), held)
	}

	// A network failure belongs to the offline path, not to "held".
	before := lines()
	s.recordCommitFailure("commit attempt failed", errors.New("svn: connection refused"))
	if lines() != before || len(held) != 1 {
		t.Fatalf("network failure counted as held: lines=%d held=%v", lines(), held)
	}

	// A successful cycle ends the episode and says so.
	s.publicationSucceeded()
	if !reflect.DeepEqual(held, []bool{true, false}) {
		t.Fatalf("after success held = %v", held)
	}
	s.publicationSucceeded()
	if len(held) != 2 {
		t.Fatalf("success without a held episode notified: %v", held)
	}

	// After recovery the old failure is new again: journaled, and held once more
	// only after two cycles.
	s.recordCommitFailure("commit attempt failed", stuck)
	if lines() != 3 || len(held) != 2 {
		t.Fatalf("new episode: lines=%d held=%v", lines(), held)
	}
	s.recordCommitFailure("commit attempt failed", stuck)
	if !reflect.DeepEqual(held, []bool{true, false, true}) {
		t.Fatalf("new episode held = %v", held)
	}
}

// Nothing listens in tests and old wirings; reporting must not require it.
func TestHeldPublicationWithoutListener(t *testing.T) {
	s := &Service{}
	for range 3 {
		s.recordCommitFailure("commit attempt failed", errors.New("publication held"))
	}
	s.publicationSucceeded()
}
