//go:build !windows

package intake

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCapacityReceiveBarrierWaitsForRealCompletion(t *testing.T) {
	root := t.TempDir()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithReceiveBarrier(context.Background(), root, false, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	called := false
	err := WithReceiveBarrier(ctx, root, true, func() error { called = true; return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatal(err, called)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := WithReceiveBarrier(context.Background(), root, true, func() error { called = true; return nil }); err != nil || !called {
		t.Fatal(err, called)
	}
}
