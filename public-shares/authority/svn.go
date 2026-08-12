package authority

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// SVNLookSource reads an exact canonical repository path without any
// credential. It belongs only on the authoritative FileES host.
type SVNLookSource struct {
	SVNLook          string
	RepositoriesRoot string
}

func (s SVNLookSource) Head(ctx context.Context, repoID string) (int64, error) {
	repository, err := s.repositoryPath(repoID)
	if err != nil {
		return 0, err
	}
	command := s.command(ctx, "youngest", repository)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &limitedWriter{Writer: &stderr, Remaining: 4096}
	if err := command.Run(); err != nil {
		return 0, fmt.Errorf("svnlook youngest: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(stdout.String()), 10, 64)
	if err != nil || revision < 0 {
		return 0, errors.New("svnlook youngest returned an invalid revision")
	}
	return revision, nil
}

func (s SVNLookSource) Cat(ctx context.Context, repoID, repoPath string, revision int64, dst io.Writer) error {
	repository, err := s.repositoryPath(repoID)
	if err != nil {
		return err
	}
	if revision < 1 || !canonicalRepoPath(repoPath) {
		return errors.New("svnlook cat request is invalid")
	}
	command := s.command(ctx, "cat", "-r", strconv.FormatInt(revision, 10), repository, repoPath)
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = dst, &limitedWriter{Writer: &stderr, Remaining: 4096}
	if err := command.Run(); err != nil {
		return fmt.Errorf("svnlook cat: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s SVNLookSource) command(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, s.SVNLook, args...)
	command.Env = svnLookEnvironment(os.Environ())
	return command
}

func svnLookEnvironment(environ []string) []string {
	result := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if strings.HasPrefix(entry, "LC_ALL=") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "LC_ALL=C.UTF-8")
}

func canonicalRepoPath(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.ContainsAny(value, "\\\x00") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func (s SVNLookSource) repositoryPath(repoID string) (string, error) {
	if !filepath.IsAbs(s.SVNLook) || !filepath.IsAbs(s.RepositoriesRoot) {
		return "", errors.New("svnlook source is incomplete")
	}
	if parsed, err := uuid.Parse(repoID); err != nil || parsed.String() != repoID {
		return "", errors.New("svnlook repository id is invalid")
	}
	root := filepath.Clean(s.RepositoriesRoot)
	target := filepath.Join(root, repoID)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel != repoID {
		return "", errors.New("svnlook repository path escapes root")
	}
	return target, nil
}

type limitedWriter struct {
	io.Writer
	Remaining int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.Remaining > 0 {
		part := p
		if int64(len(part)) > w.Remaining {
			part = part[:w.Remaining]
		}
		_, _ = w.Writer.Write(part)
		w.Remaining -= int64(len(part))
	}
	return original, nil
}
