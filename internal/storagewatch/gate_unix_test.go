//go:build linux || openbsd

package storagewatch

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCapacityGateDrainsAndRechecksAdmissions(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- g.WithWriter(context.Background(), func() error { close(entered); <-release; return nil })
	}()
	<-entered
	if err := g.Hold(); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := g.WithWriter(context.Background(), func() error { called = true; return nil }); !errors.Is(err, ErrWriteHeld) || called {
		t.Fatal(err, called)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	err := g.WithExclusive(ctx, func() error { called = true; return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatal(err, called)
	}
	if held, err := g.Held(); !held || err != nil {
		t.Fatal(held, err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := g.WithExclusive(context.Background(), g.AcknowledgeHeld); err != nil {
		t.Fatal(err)
	}
	if phase, err := g.Phase(); phase != "held" || err != nil {
		t.Fatal(phase, err)
	}
	if err := g.WithExclusive(context.Background(), g.Resume); err != nil {
		t.Fatal(err)
	}
	if err := g.WithWriter(context.Background(), func() error { called = true; return nil }); err != nil || !called {
		t.Fatal(err, called)
	}
}

func TestCapacityOrphanWriterCannotBecomeAnAcknowledgedStop(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	child := exec.Command("/bin/sh", "-c", "printf ready; read finish")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); child.Wait() }()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_ = g.WithWriter(context.Background(), func() error {
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(output, make([]byte, 5)); err != nil {
				t.Fatal(err)
			}
			panic("synthetic parent termination before child completion")
		})
	}()
	if !panicked {
		t.Fatal("fixture did not leave an unacknowledged operation")
	}
	if err := g.Hold(); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := g.WithExclusive(context.Background(), func() error { called = true; return g.AcknowledgeHeld() }); !errors.Is(err, ErrUnfinishedWriter) || called {
		t.Fatal("orphan child treated as a completed operation", err, called)
	}
	input.Close()
	_ = child.Wait()
	// Even a child exit is not guessed from PID/age. Only an operator can
	// resolve this durable evidence after quiescing all writers and children.
	if err := g.WithExclusive(context.Background(), g.Resume); !errors.Is(err, ErrUnfinishedWriter) {
		t.Fatal("unacknowledged marker automatically cleared", err)
	}
	if phase, err := g.Phase(); phase != "draining" || err != nil {
		t.Fatal(phase, err)
	}
}

func TestCapacityGateBlockedAdmissionAfterWaiting(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- g.WithExclusive(context.Background(), func() error { close(locked); <-release; return g.Hold() })
	}()
	<-locked
	writer := make(chan error, 1)
	go func() {
		writer <- g.WithWriter(context.Background(), func() error { return errors.New("admitted incorrectly") })
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-writer; !errors.Is(err, ErrWriteHeld) {
		t.Fatal(err)
	}
}

func TestCapacityGateCorruptPolicyAndSymlinkLockFailClosed(t *testing.T) {
	g := Gate{Root: t.TempDir()}
	if err := os.WriteFile(filepath.Join(g.Root, "capacity-write-policy.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if held, err := g.Held(); !held || err == nil {
		t.Fatal(held, err)
	}
	if err := g.WithWriter(context.Background(), func() error { t.Fatal("admitted"); return nil }); err == nil {
		t.Fatal("corrupt policy admitted")
	}
	g = Gate{Root: t.TempDir()}
	other := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(other, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(g.Root, ".capacity-writers.lock")); err != nil {
		t.Fatal(err)
	}
	if err := g.Prepare(context.Background()); err == nil {
		t.Fatal("symlink lock accepted")
	}
}
