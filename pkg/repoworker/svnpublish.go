package repoworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type SVNPublishRunner struct{ SVN, WorkingCopy string }

func (r SVNPublishRunner) Publish(ctx context.Context, paths []string, message string) error {
	if !filepath.IsAbs(r.SVN) || !filepath.IsAbs(r.WorkingCopy) {
		return errors.New("svn publisher paths must be absolute")
	}
	wc := filepath.Clean(r.WorkingCopy)
	commitPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		clean := filepath.Clean(p)
		if clean != wc && !strings.HasPrefix(clean, wc+string(filepath.Separator)) { // authz may intentionally live beside the service WC and is not versioned.
			continue
		}
		commitPaths = append(commitPaths, clean)
		out, err := exec.CommandContext(ctx, r.SVN, append([]string{"add", "--force", "--parents", "--non-interactive", "--no-auth-cache"}, clean)...).CombinedOutput()
		if err != nil {
			r.rollback(context.Background(), commitPaths)
			return fmt.Errorf("svn add authority: %w: %s", err, string(out))
		}
	}
	if len(commitPaths) == 0 {
		return nil
	}
	targets := r.commitTargets(ctx, wc, commitPaths)
	args := []string{"commit", "--depth", "empty", "--non-interactive", "--no-auth-cache", "-m", message}
	args = append(args, targets...)
	out, err := exec.CommandContext(ctx, r.SVN, args...).CombinedOutput()
	if err != nil {
		r.rollback(context.Background(), commitPaths)
		return fmt.Errorf("svn commit authority: %w: %s", err, string(out))
	}
	return nil
}

func (r SVNPublishRunner) commitTargets(ctx context.Context, wc string, paths []string) []string {
	seen := make(map[string]bool, len(paths)*2)
	var parents []string
	for _, path := range paths {
		for parent := filepath.Dir(path); parent != wc && strings.HasPrefix(parent, wc+string(filepath.Separator)); parent = filepath.Dir(parent) {
			if seen[parent] {
				continue
			}
			status, err := exec.CommandContext(ctx, r.SVN, "status", "--depth", "empty", "--non-interactive", "--no-auth-cache", parent).CombinedOutput()
			if err == nil && len(status) > 0 && status[0] == 'A' {
				seen[parent] = true
				parents = append(parents, parent)
			}
		}
	}
	// Parents are discovered child-first. SVN requires a newly added parent
	// to be part of the same commit, and --depth empty prevents it from
	// absorbing any unrelated scheduled descendants.
	for i, j := 0, len(parents)-1; i < j; i, j = i+1, j-1 {
		parents[i], parents[j] = parents[j], parents[i]
	}
	for _, path := range paths {
		if !seen[path] {
			seen[path] = true
			parents = append(parents, path)
		}
	}
	return parents
}

// rollback restores only the authority paths owned by this publication. It
// intentionally does not touch the rest of the shared service working copy.
// Files that were newly scheduled for addition become unversioned after
// revert and are then removed, so later projections cannot discover them.
func (r SVNPublishRunner) rollback(ctx context.Context, paths []string) {
	for i := len(paths) - 1; i >= 0; i-- {
		path := paths[i]
		_, _ = exec.CommandContext(ctx, r.SVN, "revert", "-R", "--non-interactive", "--no-auth-cache", path).CombinedOutput()
		status, err := exec.CommandContext(ctx, r.SVN, "status", "--no-ignore", "--non-interactive", "--no-auth-cache", path).CombinedOutput()
		if err == nil && len(status) > 0 && (status[0] == '?' || status[0] == 'I') {
			_ = os.RemoveAll(path)
		}
	}
}
