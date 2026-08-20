package repoworker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
)

// ReconcileServiceWorkingCopy restores the service authority WC to the
// repository state. Callers must hold the installation-wide service-WC lock.
// Unversioned files are derived, incomplete transaction output and must not be
// allowed to participate in canonical projection scans.
func ReconcileServiceWorkingCopy(ctx context.Context, svn, workingCopy string) error {
	if !filepath.IsAbs(svn) || !filepath.IsAbs(workingCopy) {
		return errors.New("service working-copy reconciliation paths must be absolute")
	}
	// The service WC belongs to the state account. Root can bypass its 0700
	// directory mode, but running an administrative publisher as root then
	// leaves root-owned pristine objects in .svn. The next correctly isolated
	// worker cannot clean those objects and every owner operation (grants,
	// branding, public shares) fails before dispatch. Refuse the wrong effective
	// identity before Subversion gets a chance to mutate the WC.
	if err := verifyServiceWorkingCopyOwner(workingCopy); err != nil {
		return err
	}
	commands := [][]string{
		{"cleanup", "--non-interactive", workingCopy},
		{"revert", "-R", "--non-interactive", "--no-auth-cache", workingCopy},
		{"cleanup", "--remove-unversioned", "--remove-ignored", "--non-interactive", workingCopy},
		{"update", "--non-interactive", "--no-auth-cache", workingCopy},
	}
	for _, args := range commands {
		out, err := exec.CommandContext(ctx, svn, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("svn %s service working copy: %w: %s", args[0], err, string(out))
		}
	}
	return nil
}
