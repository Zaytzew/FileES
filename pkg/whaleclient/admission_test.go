package whaleclient

import (
	"context"
	"testing"
	"time"

	"filees/pkg/runtime"
)

func TestAdmissionDefersWhaleLaunchThenTracksCompletion(t *testing.T) {
	var admission runtime.Admission
	resume, err := admission.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	m := &Manager{Admission: &admission, cancels: make(map[string]context.CancelFunc)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started, finish, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(finish)
	m.launch(ctx, "test", func(context.Context, string) { close(started); <-finish; close(finished) })
	select {
	case <-started:
		t.Fatal("launched through barrier")
	case <-time.After(20 * time.Millisecond):
	}
	resume()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if admission.Snapshot().Active != 1 {
		t.Fatal("whale not tracked")
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer drainCancel()
	if resume, err := admission.Quiesce(drainCtx); err == nil {
		resume()
		t.Fatal("drained while whale active")
	}
	select {
	case <-finished:
		t.Fatal("drain cancelled whale")
	default:
	}
}
