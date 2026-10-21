package svnrotate

import (
	"errors"
	"os"
	"path/filepath"
)

// These generation-changing operations bypass commit hooks. They must not
// replace an archive, including one whose local guard was administratively
// released: canonical unfreeze needs a separate, audited server workflow.
// Keep this low-level check independent of repoworker (which imports us).
func refuseArchiveGeneration(paths ...string) error {
	for _, root := range paths {
		if !filepath.IsAbs(root) {
			return errors.New("archive guard requires absolute generation path")
		}
		_, err := os.Lstat(filepath.Join(root, "hooks", ".filees-archive-protection"))
		if err == nil {
			return errors.New("archive generation replacement is not permitted")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
