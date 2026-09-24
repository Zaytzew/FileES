package commit

import (
	"testing"
	"time"
)

// A publication or update that hangs on an unreachable server holds wcOpMu for
// as long as the connect takes to time out. repo.status asks
// CommitRecoveryRequired on every interface refresh, and waiting there made
// the interface declare the daemon unreachable during a server outage
// (2026-09-24). While the lock is busy the last answer is returned at once.
func TestCommitRecoveryRequiredDoesNotWaitForARunningPublication(t *testing.T) {
	s := &Service{wc: t.TempDir()}
	if s.CommitRecoveryRequired() {
		t.Fatal("a working copy without an intent needs no recovery")
	}

	s.wcOpMu.Lock() // the hanging publication
	defer s.wcOpMu.Unlock()
	answered := make(chan bool, 1)
	go func() { answered <- s.CommitRecoveryRequired() }()
	select {
	case required := <-answered:
		if required {
			t.Fatal("the busy answer changed the last known one")
		}
	case <-time.After(time.Second):
		t.Fatal("repo.status waited for the publication holding the working copy")
	}
}
