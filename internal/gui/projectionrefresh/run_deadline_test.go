package projectionrefresh

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A callback that never returns on its own must not take its server's lane
// down with it. Schedule only raises pending for a server already present in
// the map, so without a per-run deadline one wedged refresh means that server
// is never polled again until the daemon restarts.
func TestStuckRefreshReleasesTheServerLane(t *testing.T) {
	var starts int32
	release := make(chan struct{})
	scheduler := New(context.Background(), func(ctx context.Context, serverID string) {
		if atomic.AddInt32(&starts, 1) == 1 {
			<-ctx.Done() // first pass hangs until its deadline expires
			close(release)
			return
		}
	})
	scheduler.runTimeout = 150 * time.Millisecond
	defer scheduler.Close()

	if !scheduler.Schedule("spot") {
		t.Fatal("first schedule refused")
	}
	select {
	case <-release:
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck refresh was never released by its deadline")
	}

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&starts) < 2 && time.Now().Before(deadline) {
		scheduler.Schedule("spot")
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&starts) < 2 {
		t.Fatal("the lane never ran again after a stuck refresh")
	}
}
