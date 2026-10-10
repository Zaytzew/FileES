//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"filees/pkg/commit"
	"filees/pkg/localrepo"
)

func TestAnchorMoveWaitsForObserverAndThrottlesTicksThroughCompletion(t *testing.T) {
	m := &anchorManager{localIntentMu: &sync.Mutex{}}
	observer, err := m.enterObserverIntent()
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		leave, err := m.enterMoveIntent(t.Context())
		if err != nil {
			t.Error(err)
		}
		acquired <- leave
	}()
	until := time.Now().Add(time.Second)
	for m.moveWaiters.Load() != 1 {
		if time.Now().After(until) {
			t.Fatal("move did not wait for admission")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := m.enterObserverIntent(); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal("observer overtook waiting move", err)
	}
	select {
	case <-acquired:
		t.Fatal("move bypassed active observation")
	default:
	}
	observer()
	var move func()
	select {
	case move = <-acquired:
	case <-time.After(time.Second):
		t.Fatal("move did not resume")
	}
	if move == nil {
		t.Fatal("missing move guard")
	}
	// The owner keeps this same guard across remote transfer, native grant,
	// OS completion and scanner ACK: ticks may not steal it between phases.
	for _, phase := range []string{"transfer", "grant", "OS completion", "scanner ACK"} {
		if _, err := m.enterObserverIntent(); !errors.Is(err, commit.ErrLocalStatusBusy) {
			t.Fatal(phase, "observer entered active move", err)
		}
		cursor := "unchanged"
		if err := m.refreshLocalStates(t.Context(), localrepo.Record{LocalPath: "must-not-be-walked"}, &anchorBridge{}, &cursor); !errors.Is(err, commit.ErrLocalStatusBusy) || cursor != "unchanged" {
			t.Fatal(phase, "local tick reached helper or lost cursor", cursor, err)
		}
	}
	move()
	observer, err = m.enterObserverIntent()
	if err != nil {
		t.Fatal("observation did not resume", err)
	}
	observer()
}

func TestAnchorMoveAdmissionCancelsWithoutOperationOrLeakedPriority(t *testing.T) {
	m := &anchorManager{localIntentMu: &sync.Mutex{}}
	m.localIntentMu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	leave, err := m.enterMoveIntent(ctx)
	if leave != nil || !errors.Is(err, context.DeadlineExceeded) || m.moveWaiters.Load() != 0 {
		t.Fatal("deadline leaked admission", leave != nil, err, m.moveWaiters.Load())
	}
	m.localIntentMu.Unlock()
	observer, err := m.enterObserverIntent()
	if err != nil {
		t.Fatal("cancelled waiter starved observer", err)
	}
	observer()
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	for _, manager := range []*anchorManager{m, {}} {
		if leave, err := manager.enterMoveIntent(cancelled); leave != nil || !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled admission acquired lock", err)
		}
	}
}

func TestAnchorObserverYieldsEvenWhenMutexIsFree(t *testing.T) {
	m := &anchorManager{localIntentMu: &sync.Mutex{}}
	m.moveWaiters.Store(1)
	if _, err := m.enterObserverIntent(); !errors.Is(err, commit.ErrLocalStatusBusy) {
		t.Fatal("observer overtook queued move on free mutex", err)
	}
	m.moveWaiters.Store(0)
	leave, err := m.enterMoveIntent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	leave()
}
