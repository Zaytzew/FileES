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
	// NativeProgram selects the desktop native runtime; errors never retry on CLI.
	NativeProgram                               string
	RepoURL                                     string
	Timeout                                     time.Duration
	SSHIdentityFile, SSHKnownHosts, SSHHostName string
	SSHPort                                     int
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
	var sshEnv []string
	if strings.HasPrefix(url, "svn+ssh://") || s.SSHIdentityFile != "" || s.SSHKnownHosts != "" || s.SSHHostName != "" || s.SSHPort != 0 {
		var err error
		sshEnv, err = client.PinnedSSHEnvironment(os.Environ(), s.SSHIdentityFile, s.SSHKnownHosts, s.SSHPort, s.SSHHostName)
		if err != nil {
			return nil, err
		}
	}
	if s.NativeProgram != "" {
		dir, err := os.MkdirTemp("", "filees-native-cat-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "payload")
		cli := client.New(client.Options{NativeSVNPath: s.NativeProgram, Timeout: timeout, SSHIdentityFile: s.SSHIdentityFile, SSHKnownHosts: s.SSHKnownHosts, SSHPort: s.SSHPort, SSHHostName: s.SSHHostName})
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
	if sshEnv != nil {
		cmd.Env = sshEnv
	}
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

// DownloadTimeout bounds CatToFile when the caller's context has no earlier
// deadline. A client bundle is tens of megabytes; Timeout is sized for the
// small signed channel files and ended every download on a slow link
// (owner's station, 2026-09-25).
const DownloadTimeout = 30 * time.Minute

// CatToFile writes path to dest as it arrives, so a caller can report
// progress from the file's size. dest is created or truncated; on error it may
// hold a partial payload and must not be trusted without verification.
func (s SVN) CatToFile(ctx context.Context, path, dest string) error {
	program := strings.TrimSpace(s.Program)
	if program == "" {
		program = "svn"
	}
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	url := joinURL(s.RepoURL, path)
	if s.NativeProgram != "" {
		cli := client.New(client.Options{NativeSVNPath: s.NativeProgram, Timeout: DownloadTimeout, SSHIdentityFile: s.SSHIdentityFile, SSHKnownHosts: s.SSHKnownHosts, SSHPort: s.SSHPort, SSHHostName: s.SSHHostName})
		return cli.(interface {
			CatTo(context.Context, string, string) error
		}).CatTo(ctx, url, dest)
	}
	var sshEnv []string
	if strings.HasPrefix(url, "svn+ssh://") || s.SSHIdentityFile != "" || s.SSHKnownHosts != "" || s.SSHHostName != "" || s.SSHPort != 0 {
		var err error
		sshEnv, err = client.PinnedSSHEnvironment(os.Environ(), s.SSHIdentityFile, s.SSHKnownHosts, s.SSHPort, s.SSHHostName)
		if err != nil {
			return err
		}
	}
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, program, "cat", "--non-interactive", "--no-auth-cache", url)
	if sshEnv != nil {
		cmd.Env = sshEnv
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = file, &stderr
	runErr := cmd.Run()
	closeErr := file.Close()
	if runErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("svn cat %s: %w: %s", url, runErr, msg)
		}
		return fmt.Errorf("svn cat %s: %w", url, runErr)
	}
	return closeErr
}

func joinURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	path = strings.TrimLeft(strings.TrimSpace(path), "/")
	if path == "" {
		return base
	}
	return base + "/" + path
}
