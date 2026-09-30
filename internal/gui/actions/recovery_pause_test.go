package actions

import (
	"context"
	"filees/internal/gui/tray"
	"sync/atomic"
	"testing"
)

func TestGUIRecoveryDoesNotCancelOrReplayTasks(t *testing.T) {
	intents := make(chan tray.Intent, 4)
	c := New(Config{Intents: intents})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { c.Run(ctx); close(finished) }()
	c.tasks.Add(1) // A running IPC call / dialog, not a deduplication key.
	if c.PauseIfIdle(ctx) {
		t.Fatal("paused with an active task")
	}
	if ctx.Err() != nil {
		t.Fatal("running task cancelled")
	}
	c.tasks.Add(1) // Child starts while the parent still runs.
	c.tasks.Done()
	if c.PauseIfIdle(ctx) {
		t.Fatal("paused with an active child")
	}
	c.tasks.Done()
	if !c.PauseIfIdle(ctx) {
		t.Fatal("idle dispatcher not paused")
	}
	if !c.PauseIfIdle(ctx) {
		t.Fatal("pause is not idempotent")
	}
	cancel()
	<-finished
}

func TestGUIRecoveryDrainsQueuedGestureWithoutReplay(t *testing.T) {
	intents := make(chan tray.Intent, 4)
	var calls atomic.Int32
	dispatched := make(chan struct{}, 2)
	c := New(Config{Intents: intents, Reconnect: func() { calls.Add(1); dispatched <- struct{}{} }})
	intents <- tray.Intent{Kind: tray.IntentReconnect}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { c.Run(ctx); close(finished) }()
	<-dispatched
	if !c.PauseIfIdle(ctx) {
		t.Fatal("drained controller not idle")
	}
	// Only misbehaving producers can send now: the composition has already
	// stopped admission. Even then, recovery must not dispatch the gesture.
	intents <- tray.Intent{Kind: tray.IntentReconnect}
	if !c.PauseIfIdle(ctx) {
		t.Fatal("pause lost")
	}
	cancel()
	<-finished
	if calls.Load() != 1 {
		t.Fatalf("accepted gesture replayed: %d", calls.Load())
	}
}

func TestGUIRecoveryDoesNotPauseUnstartedDispatcher(t *testing.T) {
	c := New(Config{Intents: make(chan tray.Intent, 1)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.PauseIfIdle(ctx) {
		t.Fatal("unstarted dispatcher reported idle")
	}
}
