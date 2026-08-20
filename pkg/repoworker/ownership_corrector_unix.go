//go:build !windows

package repoworker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// OwnershipCorrection summarizes one bounded service-WC ownership pass.
type OwnershipCorrection struct {
	Inspected int `json:"inspected"`
	Corrected int `json:"corrected"`
}

// CorrectServiceWorkingCopyOwnership repairs only ownership, never content or
// modes. It is intended for the tiny setuid-root corrector invoked before a
// repository worker; ordinary workers must remain unprivileged.
func CorrectServiceWorkingCopyOwnership(activationRoot, workingCopy string) (OwnershipCorrection, error) {
	if os.Geteuid() != 0 {
		return OwnershipCorrection{}, errors.New("service working-copy ownership correction requires effective uid 0")
	}
	if !filepath.IsAbs(activationRoot) || !filepath.IsAbs(workingCopy) {
		return OwnershipCorrection{}, errors.New("service working-copy ownership paths must be absolute")
	}
	owner, err := ownershipIdentity(activationRoot, true)
	if err != nil {
		return OwnershipCorrection{}, fmt.Errorf("service-state owner: %w", err)
	}
	if owner.uid == 0 {
		return OwnershipCorrection{}, errors.New("service-state owner must not be root")
	}
	wc, err := ownershipIdentity(workingCopy, true)
	if err != nil {
		return OwnershipCorrection{}, fmt.Errorf("service working copy: %w", err)
	}
	lockPath := filepath.Join(activationRoot, ".service-wc.lock")
	var result OwnershipCorrection
	err = WithFileLock(lockPath, func() error {
		if err := os.Lchown(lockPath, owner.uid, owner.gid); err != nil {
			return fmt.Errorf("correct service working-copy lock owner: %w", err)
		}
		return filepath.WalkDir(workingCopy, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return fmt.Errorf("service working-copy path %s exposes no ownership information", path)
			}
			if uint64(stat.Dev) != wc.dev {
				return fmt.Errorf("service working-copy path %s crosses a filesystem boundary", path)
			}
			mode := info.Mode()
			if !mode.IsDir() && !mode.IsRegular() && mode&os.ModeSymlink == 0 {
				return fmt.Errorf("service working-copy path %s has unsupported type %s", path, mode.Type())
			}
			result.Inspected++
			if int(stat.Uid) == owner.uid && int(stat.Gid) == owner.gid {
				return nil
			}
			if err := os.Lchown(path, owner.uid, owner.gid); err != nil {
				return fmt.Errorf("correct service working-copy owner %s: %w", path, err)
			}
			result.Corrected++
			return nil
		})
	})
	return result, err
}

type pathOwnership struct {
	uid, gid int
	dev      uint64
}

func ownershipIdentity(path string, requireDirectory bool) (pathOwnership, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return pathOwnership{}, err
	}
	if requireDirectory && !info.IsDir() {
		return pathOwnership{}, errors.New("path is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return pathOwnership{}, errors.New("path exposes no ownership information")
	}
	return pathOwnership{uid: int(stat.Uid), gid: int(stat.Gid), dev: uint64(stat.Dev)}, nil
}
