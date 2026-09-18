package repoworker

import (
	"path/filepath"
	"testing"
	"time"
)

// A server where nobody has shared anything yet has no outbox directory. The
// delivery run must find nothing to send, not fail every time it looks.
func TestAnOutboxThatWasNeverCreatedIsEmpty(t *testing.T) {
	outbox := PublicShareOutbox{Root: filepath.Join(t.TempDir(), "outbox")}
	job, found, err := outbox.Claim(time.Now(), time.Minute)
	if err != nil || found {
		t.Fatalf("claim on a missing outbox: job=%+v found=%v err=%v", job, found, err)
	}
}
