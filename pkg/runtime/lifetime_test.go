package runtime

import (
	"context"
	"testing"
	"time"
)

func TestLifetimeJoinsFinalizerAfterCancellation(t *testing.T) {
	l := &Lifetime{Admission: &Admission{}}
	ctx, cancel := context.WithCancel(WithLifetime(context.Background(), l))
	defer cancel()
	finish := make(chan struct{})
	Go(ctx, func() { <-ctx.Done(); <-finish })
	l.BeginMemoryStop()
	cancel()
	if !MemoryStopping(ctx) {
		t.Fatal("memory shutdown lost")
	}
	done := make(chan struct{})
	go func() { l.Wait(); close(done) }()
	select {
	case <-done:
		t.Fatal("finalizer not joined")
	case <-time.After(10 * time.Millisecond):
	}
	close(finish)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("join stuck")
	}
}
