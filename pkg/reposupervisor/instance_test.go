package reposupervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedInstanceStopWaitsForRunThenCleansExactlyOnce(t *testing.T) {
	release := make(chan struct{})
	runExited := make(chan struct{})
	var cleanups atomic.Int32
	instance, err := StartManaged(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		<-release
		close(runExited)
		return ctx.Err()
	}, func(context.Context) error {
		select {
		case <-runExited:
		default:
			t.Error("cleanup ran before pipeline exited")
		}
		cleanups.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- instance.Stop(context.Background()) }()
	select {
	case err := <-firstDone:
		t.Fatalf("Stop returned early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if cleanups.Load() != 1 {
		t.Fatalf("cleanups=%d", cleanups.Load())
	}
}

func TestManagedInstanceStopDeadlineDoesNotSkipLaterCleanup(t *testing.T) {
	release := make(chan struct{})
	cleaned := make(chan struct{})
	instance, _ := StartManaged(context.Background(), func(ctx context.Context) error { <-ctx.Done(); <-release; return nil }, func(context.Context) error { close(cleaned); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := instance.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error=%v", err)
	}
	close(release)
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("cleanup not completed")
	}
}
