package repoworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var errDumpStreamLimit = errors.New("LOAD_REPOSITORY_DUMP: dump stream exceeds byte limit")

func (s DumpLoadService) writeDumpStream(ctx context.Context, dst string, src *string, bin string, args ...string) error {
	limit := s.MaxDumpBytes
	if limit == 0 {
		limit = -1
	}
	err := runToFile(ctx, dst, src, limit, bin, args...)
	if errors.Is(err, errDumpStreamLimit) {
		return fmt.Errorf("repositories.max_dump_size (%d bytes): %w", s.MaxDumpBytes, err)
	}
	return err
}

// runToFile owns only the file it creates. The child gets a pipe, never the
// output file: all bytes pass through the bound, including io.Copy fast paths.
// A negative limit disables the bound; zero permits only empty output.
func runToFile(ctx context.Context, dst string, src *string, limit int64, bin string, args ...string) (retErr error) {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		if retErr != nil {
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				retErr = errors.Join(retErr, fmt.Errorf("remove incomplete dump: %w", err))
			}
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	// Also bound pipe draining if a descendant inherits stdout/stderr.
	cmd.WaitDelay = 2 * time.Second
	if src != nil {
		in, err := os.Open(*src)
		if err != nil {
			return err
		}
		defer in.Close()
		cmd.Stdin = in
	}
	var diagnostic dumpOutput
	w := &dumpStreamWriter{dst: out, remaining: limit, cancel: cancel}
	cmd.Stdout, cmd.Stderr = w, &diagnostic
	runErr := cmd.Run() // Wait for the child and the Go writer before cleanup.
	if w.err != nil {
		return w.err // Preserve the limit/disk error, not the resulting kill.
	}
	if runErr != nil {
		return fmt.Errorf("%s %s: %w: %s", filepath.Base(bin), strings.Join(args, " "), runErr, strings.TrimSpace(diagnostic.String()))
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

type dumpStreamWriter struct {
	dst       io.Writer
	remaining int64
	cancel    context.CancelFunc
	err       error
}

func (w *dumpStreamWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	over := w.remaining >= 0 && int64(len(p)) > w.remaining
	if over {
		p = p[:w.remaining]
	}
	n, err := w.dst.Write(p)
	if w.remaining >= 0 {
		w.remaining -= int64(n)
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err == nil && over {
		err = errDumpStreamLimit
	}
	if err != nil {
		w.err = err
		w.cancel()
	}
	return n, err
}
