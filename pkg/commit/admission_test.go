package commit

import (
	"context"
	"errors"
	"testing"
	"time"

	"filees/pkg/runtime"
)

func TestAdmissionBlocksCommitAndPollBeforeClientAccess(t *testing.T) {
	var admission runtime.Admission
	resume, err := admission.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	s := Service{Admission: &admission} // nil client must never be called while closed
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.tryCommitMode(ctx, t.TempDir(), true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("commit: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	s.pollOnce(ctx2, t.TempDir(), "unused")
	if s := admission.Snapshot(); s.Active != 0 || !s.Closed {
		t.Fatal(s)
	}
}
