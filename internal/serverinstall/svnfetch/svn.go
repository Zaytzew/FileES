package svnfetch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
)

type Fetcher interface {
	Cat(ctx context.Context, path string) ([]byte, error)
}

type SVN struct {
	Program string
	// NativeProgram is an explicit Windows opt-in; errors never retry on CLI.
	NativeProgram string
	RepoURL       string
	Timeout       time.Duration
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
	if s.NativeProgram != "" {
		dir, err := os.MkdirTemp("", "filees-native-cat-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "payload")
		cli := client.New(client.Options{NativeSVNPath: s.NativeProgram, Timeout: timeout})
		err = cli.(interface {
			CatTo(context.Context, string, string) error
		}).CatTo(ctx, url, out)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(out)
	}
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
