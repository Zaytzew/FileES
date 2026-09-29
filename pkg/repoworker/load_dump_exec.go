package repoworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// Drain diagnostics without retaining an unbounded child-process buffer.
// Small metadata commands must fail rather than interpret truncated stdout.
type dumpOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *dumpOutput) Len() int      { return b.buffer.Len() }
func (b *dumpOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *dumpOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (b *dumpOutput) String() string {
	if b.truncated {
		return b.buffer.String() + " [truncated]"
	}
	return b.buffer.String()
}

func runDumpTool(ctx context.Context, in io.Reader, out io.Writer, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout = in, out
	var diagnostic dumpOutput
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, strings.TrimSpace(diagnostic.String()))
	}
	return nil
}

func dumpSmallOutput(ctx context.Context, bin string, args ...string) ([]byte, error) {
	var out dumpOutput
	if err := runDumpTool(ctx, nil, &out, bin, args...); err != nil {
		return nil, err
	}
	if out.truncated {
		return nil, errors.New("dump tool metadata output exceeds 64 KiB")
	}
	return out.Bytes(), nil
}
