package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"filees/internal/serverinstall/platform"
)

// repositoriesRoot is server.json's repositories.root; empty when the server
// has none configured yet.
func (r *Runner) repositoriesRoot() (string, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.SysconfDir, "server.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var document struct {
		Repositories struct {
			Root string `json:"root"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", fmt.Errorf("read repositories root from server.json: %w", err)
	}
	root := document.Repositories.Root
	if root == "" {
		return "", nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return "", fmt.Errorf("repositories.root %q must be an absolute dedicated directory", root)
	}
	return filepath.Clean(root), nil
}

// repositoryOwnershipReportLimit bounds the per-file lines; the total is
// always reported.
const repositoryOwnershipReportLimit = 20

// correctRepositoryOwnership gives every entry under the repositories root
// back to the root's owner, the account the repository worker runs as.
//
// spot, 2026-09-25: an administrative svnadmin run as root on 22.08 left
// db/rep-cache.db and its journal owned by root; svnadmin freeze could not
// write them and every deletion of that repository failed until the owner
// chowned them by hand. Only the owning user is changed — never the group,
// the mode or the content — and every change is printed: existing ownership
// is not corrected silently. A dry run only reports.
func (r *Runner) correctRepositoryOwnership(root string, dryRun bool) error {
	if root == "" {
		return nil
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("repositories root %s is not a directory", root)
	}
	owners := r.ownershipManager()
	want, err := owners.Stat(root)
	if err != nil {
		return err
	}
	if want.UID == 0 {
		fmt.Fprintf(r.Out, "[REPO-OWNER] %s is owned by root; repository ownership is not corrected (expected the repository worker's account)\n", root)
		return nil
	}
	verb := "corrected"
	if dryRun {
		verb = "would correct"
	}
	inspected, corrected := 0, 0
	err = filepath.WalkDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		inspected++
		current, err := owners.Stat(path)
		if err != nil {
			return err
		}
		if current.UID == want.UID {
			return nil
		}
		corrected++
		if corrected <= repositoryOwnershipReportLimit {
			fmt.Fprintf(r.Out, "[REPO-OWNER] %s %s: uid %d -> %d\n", verb, path, current.UID, want.UID)
		}
		if dryRun {
			return nil
		}
		if err := owners.Apply(path, platform.Ownership{UID: want.UID, GID: current.GID}); err != nil {
			return fmt.Errorf("correct owner of %s: %w", path, err)
		}
		return nil
	})
	if corrected > 0 || r.Config.Talkative {
		fmt.Fprintf(r.Out, "[REPO-OWNER] %s %d of %d entries under %s\n", verb, corrected, inspected, root)
	}
	return err
}
