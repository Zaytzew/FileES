package actions

import (
	"context"
	"filees/internal/gui/platform/platformtest"
	"strings"
	"testing"
	"time"
)

type queueThenCompleteRelocator struct{ polls int }

func (*queueThenCompleteRelocator) RelocateRepository(context.Context, string, string, string) (string, error) {
	return "op", nil
}
func (r *queueThenCompleteRelocator) RelocationStatus(context.Context, string) (string, string, error) {
	r.polls++
	if r.polls < 3 {
		return "relocation_queued", "", nil
	}
	return "attached", "", nil
}

func TestQueuedRelocationNotifiesWaitingBeforeCompletion(t *testing.T) {
	fake := &platformtest.Fake{}
	r := &queueThenCompleteRelocator{}
	c := New(Config{Notifier: fake, Prompter: fake, RepositoryRelocator: r, CreationStatusPollInterval: time.Millisecond, CreationStatusPollTimeout: time.Second})
	c.awaitRelocationOutcome(t.Context(), "move", "Project", "new", "op")
	s := fake.Snapshot()
	if len(s.InfoRequests) != 0 || len(s.Notifications) != 2 || !strings.Contains(s.Notifications[0].Title, "kolejce") || !strings.Contains(s.Notifications[1].Title, "przeniesiony") {
		t.Fatalf("snapshot=%+v", s)
	}
}
