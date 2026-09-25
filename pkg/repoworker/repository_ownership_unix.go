//go:build !windows

package repoworker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// CheckRepositoryOwnership reports the first entry of repo not owned by this
// process. It changes nothing; the worker is unprivileged by design and the
// correction belongs to the installer.
func CheckRepositoryOwnership(repo string) error {
	want := os.Geteuid()
	return filepath.WalkDir(repo, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("repository path exposes no ownership information")
		}
		if int(stat.Uid) == want {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			rel = path
		}
		return &RepositoryOwnershipError{Repo: repo, Path: filepath.ToSlash(rel), UID: int(stat.Uid), WantUID: want}
	})
}
