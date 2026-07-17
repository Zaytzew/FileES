// Package signify verifies detached signify(1) signatures over files
// filees-install fetches from the FILESS-BIN repository.
//
// Verification shells out to the real signify(1) binary instead of
// reimplementing its format. OpenBSD ships signify in base; on Linux
// signify-openbsd provides the same interface.
package signify

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Verifier interface {
	Verify(ctx context.Context, pubkeyPath, messagePath, sigPath string) error
}

type CLI struct {
	Program string
	Timeout time.Duration
}

func (c CLI) Verify(ctx context.Context, pubkeyPath, messagePath, sigPath string) error {
	program := strings.TrimSpace(c.Program)
	if program == "" {
		program = "signify"
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, program, "-V", "-q", "-p", pubkeyPath, "-m", messagePath, "-x", sigPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("signify -V %s: %w: %s", messagePath, err, msg)
		}
		return fmt.Errorf("signify -V %s: %w", messagePath, err)
	}
	return nil
}
