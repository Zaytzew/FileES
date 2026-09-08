package repoworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// This is headroom, not a reservation or an estimate of the dump's size.
// Other writers can consume it; checks do not promise protection from an
// arbitrary concurrent write, quota change or device failure.
const deletionSpaceFloor int64 = 256 << 20
const deletionSpaceInterval = 250 * time.Millisecond
const deletionWriteChunk = 1 << 20

var errDeletionCapacity = errors.New("repository deletion capacity guard")

type deletionSpaceCheck func(context.Context, string, int64) error

type deletionContextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r deletionContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

func checkDeletionSpace(ctx context.Context, root string, extra int64) error {
	available, _, err := (FilesystemCapacity{Root: root}).Check(ctx, 0)
	if err != nil {
		return fmt.Errorf("%w: measure %s: %w", errDeletionCapacity, root, err)
	}
	required := saturatingAdd(deletionSpaceFloor, extra)
	if available < required {
		return fmt.Errorf("%w: %s: available=%d required=%d", errDeletionCapacity, root, available, required)
	}
	return nil
}

// runDeletionCommand owns both the subprocess and its space monitor. A pipe
// writer error cancels the whole group, rather than waiting for freeze while
// its child still holds a pipe open. Completion joins the monitor before any
// caller removes staging files. check is injectable only inside this package.
func runDeletionCommand(ctx context.Context, root string, check deletionSpaceCheck, name string, args []string, setup func(*exec.Cmd, context.Context, context.CancelCauseFunc)) error {
	if err := check(ctx, root, 0); err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = root // Do not inherit an SSH home outside locked unveil.
	command.WaitDelay = 2 * time.Second
	configureDeletionProcess(command)
	setup(command, ctx, cancel)
	done := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		ticker := time.NewTicker(deletionSpaceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := check(ctx, root, 0); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	err := command.Run()
	close(done)
	<-joined
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

// Keep the dump descriptor in the worker, not in svnadmin: after worker
// death the child's pipe breaks instead of continuing to fill an unlinked
// file. Check every bounded write, including writes faster than the timer.
type deletionDumpWriter struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	root   string
	check  deletionSpaceCheck
	out    io.Writer
}

func (w deletionDumpWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(len(p), deletionWriteChunk)
		if err := w.ctx.Err(); err != nil {
			return total, err
		}
		if err := w.check(w.ctx, w.root, int64(n)); err != nil {
			w.cancel(err)
			return total, err
		}
		written, err := w.out.Write(p[:n])
		total += written
		p = p[written:]
		if err == nil && written != n {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.cancel(err)
			return total, err
		}
	}
	return total, nil
}
