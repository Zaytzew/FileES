package main

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"time"

	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/memoryguard"
	"filees/pkg/runtime"
	"filees/pkg/talk"
)

// quiesceMemory preserves the strict ingress-before-executors order.
// Failed preparation resumes both; successful preparation remains closed.
func quiesceMemory(ctx context.Context, ipc *ipcserver.Server) (func(), error) {
	resumeRequests, err := ipc.QuiesceRequests(ctx)
	if err != nil {
		return nil, err
	}
	resumeWork, err := ipc.OperationAdmission().Quiesce(ctx)
	if err != nil {
		resumeRequests()
		return nil, err
	}
	return func() { resumeWork(); resumeRequests() }, nil
}

func runMemorySafety(root context.Context, ipc *ipcserver.Server, life *runtime.Lifetime, stopWork context.CancelFunc, supervisorResult <-chan error, restart func()) {
	newMemoryGuard(root, ipc, life, stopWork, supervisorResult, restart).Run(root)
}

func newMemoryGuard(root context.Context, ipc *ipcserver.Server, life *runtime.Lifetime, stopWork context.CancelFunc, supervisorResult <-chan error, restart func()) *memoryguard.Guard {
	lastPhase := ""
	g := memoryguard.Guard{Path: filepath.Join(filepath.Dir(clientprofile.DefaultRoot()), "memory-restart.json")}
	g.Stopped = func() bool { return runtime.MemoryStopping(runtime.WithLifetime(root, life)) }
	lastDiagnostic := ""
	g.Diagnostic = func(err error) {
		if message := err.Error(); message != lastDiagnostic {
			talk.With("memory-safety").Infof("recovery diagnostic: %s", message)
			lastDiagnostic = message
		}
	}
	g.Publish = func(s memoryguard.Status) {
		value := contract.MemorySafetyStatus{Phase: s.Phase, PrivateBytes: s.Sample.PrivateBytes, PhysicalBytes: s.Sample.TotalBytes, MeasuredAt: s.At.UTC().Format(time.RFC3339Nano)}
		if !s.LastRestart.IsZero() {
			value.LastRestartAt = s.LastRestart.UTC().Format(time.RFC3339Nano)
		}
		ipc.SetMemorySafety(value)
		if s.Phase != lastPhase {
			talk.With("memory-safety").Infof("phase=%s private=%d physical=%d heap=%d goroutines=%d", s.Phase, s.Sample.PrivateBytes, s.Sample.TotalBytes, s.Sample.HeapBytes, s.Sample.Goroutines)
			lastPhase = s.Phase
		}
	}
	g.Cleanup = func() {
		ctx, cancel := context.WithTimeout(root, 100*time.Millisecond)
		defer cancel()
		resume, err := quiesceMemory(ctx, ipc)
		if err != nil {
			return
		}
		defer resume()
		debug.FreeOSMemory()
	}
	g.Restart = func(ctx context.Context, checkpoint func() error) error {
		prepare, cancel := context.WithTimeout(ctx, 15*time.Second)
		resume, err := quiesceMemory(prepare, ipc)
		cancel()
		if err != nil {
			return err
		}
		if err = checkpoint(); err != nil {
			resume()
			return err
		}
		// No business executor is active. Cancellation now wakes idle loops;
		// local finalizers finish and are joined, without a force-kill deadline.
		life.BeginMemoryStop()
		stopWork()
		done := make(chan struct{})
		go func() { life.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := life.Err(); err != nil {
			return fmt.Errorf("final state write: %w", err)
		}
		if err := <-supervisorResult; err != nil {
			return fmt.Errorf("supervisor finalization: %w", err)
		}
		restart()
		return nil
	}
	return &g
}
