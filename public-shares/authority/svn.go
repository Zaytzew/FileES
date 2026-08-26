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
	"sync"

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

// Tree enumerates regular repository leaves below sourceRoot at one immutable
// revision. svnlook's --full-paths format marks directories with a trailing
// slash, so no working copy and no repository credential is involved.
func (s SVNLookSource) Tree(ctx context.Context, repoID, sourceRoot string, revision int64) ([]TreeObject, error) {
	repository, err := s.repositoryPath(repoID)
	if err != nil {
		return nil, err
	}
	if revision < 1 || (sourceRoot != "." && !canonicalRepoPath(sourceRoot)) {
		return nil, errors.New("svnlook tree request is invalid")
	}
	args := []string{"tree", "--full-paths", "-r", strconv.FormatInt(revision, 10), repository}
	if sourceRoot != "." {
		args = append(args, sourceRoot)
	}
	command := s.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &limitedWriter{Writer: &stderr, Remaining: 4096}
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("svnlook tree: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() > 4<<20 {
		return nil, errors.New("svnlook tree output exceeds limit")
	}
	prefix := ""
	if sourceRoot != "." {
		prefix = strings.TrimSuffix(sourceRoot, "/") + "/"
	}
	objects := make([]TreeObject, 0)
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		repoPath := strings.TrimSuffix(line, "\r")
		if repoPath == "" || strings.HasSuffix(repoPath, "/") {
			continue
		}
		if !canonicalRepoPath(repoPath) || (prefix != "" && !strings.HasPrefix(repoPath, prefix)) {
			return nil, errors.New("svnlook tree returned a path outside source root")
		}
		displayName := repoPath
		if prefix != "" {
			displayName = strings.TrimPrefix(repoPath, prefix)
		}
		objects = append(objects, TreeObject{RepoPath: repoPath, DisplayName: displayName})
		if len(objects) > 4096 {
			return nil, errors.New("svnlook tree contains more than 4096 files")
		}
	}
	if err := s.populateSizes(ctx, repository, revision, objects); err != nil {
		return nil, err
	}
	return objects, nil
}

// populateSizes uses svnlook's repository-native filesize operation so the
// listing and later download describe exactly the same immutable revision.
// A small worker pool avoids serial process latency without allowing a large
// share to create an unbounded number of svnlook children.
func (s SVNLookSource) populateSizes(ctx context.Context, repository string, revision int64, objects []TreeObject) error {
	if len(objects) == 0 {
		return nil
	}
	workers := 8
	if len(objects) < workers {
		workers = len(objects)
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				size, err := s.fileSize(workCtx, repository, objects[index].RepoPath, revision)
				if err != nil {
					select {
					case errCh <- err:
						cancel()
					default:
					}
					return
				}
				objects[index].Size = &size
			}
		}()
	}
	for index := range objects {
		select {
		case jobs <- index:
		case <-workCtx.Done():
			break
		}
		if workCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s SVNLookSource) fileSize(ctx context.Context, repository, repoPath string, revision int64) (int64, error) {
	command := s.command(ctx, "filesize", "-r", strconv.FormatInt(revision, 10), repository, repoPath)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &limitedWriter{Writer: &stdout, Remaining: 128}, &limitedWriter{Writer: &stderr, Remaining: 4096}
	if err := command.Run(); err != nil {
		return 0, fmt.Errorf("svnlook filesize %s: %w: %s", repoPath, err, strings.TrimSpace(stderr.String()))
	}
	size, err := strconv.ParseInt(strings.TrimSpace(stdout.String()), 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("svnlook filesize %s returned an invalid size", repoPath)
	}
	return size, nil
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
