package repoworker

import (
	"context"
	"errors"
	"fmt"
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
	for _, p := range paths {
		clean := filepath.Clean(p)
		if clean != wc && !strings.HasPrefix(clean, wc+string(filepath.Separator)) { // authz may intentionally live beside the service WC and is not versioned.
			continue
		}
		out, err := exec.CommandContext(ctx, r.SVN, append([]string{"add", "--force", "--parents", "--non-interactive", "--no-auth-cache"}, clean)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("svn add authority: %w: %s", err, string(out))
		}
	}
	out, err := exec.CommandContext(ctx, r.SVN, "commit", "--non-interactive", "--no-auth-cache", "-m", message, wc).CombinedOutput()
	if err != nil {
		return fmt.Errorf("svn commit authority: %w: %s", err, string(out))
	}
	return nil
}
