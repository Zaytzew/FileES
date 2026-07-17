package svnfetch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Fetcher interface {
	Cat(ctx context.Context, path string) ([]byte, error)
}

type SVN struct {
	Program string
	RepoURL string
	Timeout time.Duration
}

func (s SVN) Cat(ctx context.Context, path string) ([]byte, error) {
	program := strings.TrimSpace(s.Program)
	if program == "" {
		program = "svn"
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := joinURL(s.RepoURL, path)
	cmd := exec.CommandContext(ctx, program, "cat",
		"--non-interactive", "--no-auth-cache", url)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("svn cat %s: %w: %s", url, err, msg)
		}
		return nil, fmt.Errorf("svn cat %s: %w", url, err)
	}
	return stdout.Bytes(), nil
}

func joinURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	path = strings.TrimLeft(strings.TrimSpace(path), "/")
	if path == "" {
		return base
	}
	return base + "/" + path
}
