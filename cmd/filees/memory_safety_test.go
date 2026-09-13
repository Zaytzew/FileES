package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/ipcserver"
	"filees/pkg/memoryguard"
	"filees/pkg/runtime"
)

func TestMemoryPreparationNeverCancelsActiveExecutor(t *testing.T) {
	ipc := ipcserver.New("unused")
	release, err := ipc.OperationAdmission().Enter()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	resume, err := quiesceMemory(ctx, ipc)
	if err == nil {
		resume()
		t.Fatal("active executor drained")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if state := ipc.OperationAdmission().Snapshot(); state.Closed || state.Active != 1 {
		t.Fatal(state)
	}
	// Ingress reopened after the failed second phase.
	ingress, err := ipc.QuiesceRequests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ingress()
}

func TestMemoryRestartJoinsFinalWritesAndPersistsReceipt(t *testing.T) {
	ipc := ipcserver.New("unused")
	life := &runtime.Lifetime{Admission: ipc.OperationAdmission()}
	ctx, stop := context.WithCancel(runtime.WithLifetime(context.Background(), life))
	defer stop()
	finalize := make(chan struct{})
	defer func() {
		select {
		case <-finalize:
		default:
			close(finalize)
		}
	}()
	finalStarted := make(chan struct{})
	runtime.Go(ctx, func() { <-ctx.Done(); close(finalStarted); <-finalize })
	result := make(chan error, 1)
	result <- nil
	restarted := false
	g := newMemoryGuard(t.Context(), ipc, life, stop, result, func() { restarted = true })
	g.Path = filepath.Join(t.TempDir(), "memory.json")
	g.Cleanup = func() {}
	g.Sample = func() (memoryguard.Sample, error) {
		return memoryguard.Sample{PrivateBytes: 3 << 30, TotalBytes: 16 << 30}, nil
	}
	for i := 0; i < 3; i++ {
		if g.Step(t.Context()) {
			t.Fatal("premature restart")
		}
	}
	done := make(chan bool, 1)
	go func() { done <- g.Step(t.Context()) }()
	select {
	case <-finalStarted:
	case <-time.After(time.Second):
		t.Fatal("finalization not started")
	}
	if !runtime.MemoryStopping(ctx) {
		t.Fatal("normal publication drain selected")
	}
	select {
	case <-done:
		t.Fatal("restart before final write")
	case <-time.After(20 * time.Millisecond):
	}
	close(finalize)
	select {
	case ok := <-done:
		if !ok || !restarted {
			t.Fatal("restart missing")
		}
	case <-time.After(time.Second):
		t.Fatal("restart stuck")
	}
	loaded := memoryguard.Guard{Path: g.Path, Sample: g.Sample}
	loaded.Load()
	called := false
	loaded.Restart = func(context.Context, func() error) error { called = true; return nil }
	for i := 0; i < 6; i++ {
		loaded.Step(t.Context())
	}
	if called {
		t.Fatal("persistent cooldown failed")
	}
}

func TestMemoryFinalizationFailureKeepsTransportAndBlocksReplacement(t *testing.T) {
	for _, source := range []string{"writer", "supervisor"} {
		t.Run(source, func(t *testing.T) {
			ipc := ipcserver.New("unused")
			life := &runtime.Lifetime{Admission: ipc.OperationAdmission()}
			ctx, stop := context.WithCancel(runtime.WithLifetime(t.Context(), life))
			defer stop()
			failure := errors.New("synthetic finalization failure")
			runtime.Go(ctx, func() {
				<-ctx.Done()
				if source == "writer" {
					runtime.FinalizationError(ctx, failure)
				}
			})
			result := make(chan error, 1)
			if source == "supervisor" {
				result <- failure
			} else {
				result <- nil
			}
			g := newMemoryGuard(t.Context(), ipc, life, stop, result, func() { t.Error("unsafe process replacement") })
			g.Path = filepath.Join(t.TempDir(), "memory.json")
			g.Cleanup = func() {}
			g.Sample = func() (memoryguard.Sample, error) {
				return memoryguard.Sample{PrivateBytes: 3 << 30, TotalBytes: 16 << 30}, nil
			}
			for i := 0; i < 4; i++ {
				if g.Step(t.Context()) {
					t.Fatal("restart")
				}
			}
			if ipc.MemorySafety().Phase != "recovery_required" {
				t.Fatal(ipc.MemorySafety())
			}
			g.Sample = func() (memoryguard.Sample, error) { return memoryguard.Sample{TotalBytes: 16 << 30}, nil }
			g.Step(t.Context())
			if ipc.MemorySafety().Phase != "recovery_required" {
				t.Fatal("low memory falsely cleared stopped-worker warning")
			}
			if !ipc.OperationAdmission().Snapshot().Closed {
				t.Fatal("stopped workers re-admitted")
			}
		})
	}
}
