// Package storage checks the actual filesystem used for Public Shares bytes.
// It grants no access, chooses no volume and never evicts user data.
package storage

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

var ErrUnavailable = errors.New("public download storage unavailable")

// RequireSpace is an advisory admission check, not a disk reservation.
// Writers must still propagate ENOSPC: other processes can consume space.
func RequireSpace(path string, size int64) error {
	available, err := Available(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return CheckSpace(available, size)
}

func CheckSpace(available, size int64) error {
	const margin = int64(16 << 20)
	if size < 0 || available < margin || size > available-margin {
		return fmt.Errorf("%w: available=%d required=%d reserve=%d", ErrUnavailable, available, size, margin)
	}
	return nil
}

func product(blocks, size uint64) int64 {
	if size != 0 && blocks > math.MaxInt64/size {
		return math.MaxInt64
	}
	return int64(blocks * size)
}

// PersistentRoot rejects scratch paths, including paths reached through a
// symlink. Missing suffixes are resolved against their existing ancestor.
func PersistentRoot(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return errors.New("public download root must be an absolute dedicated directory")
	}
	probe, suffix := filepath.Clean(path), ""
	for {
		_, err := os.Lstat(probe)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return err
		}
		suffix = filepath.Join(filepath.Base(probe), suffix)
		probe = parent
	}
	real, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return err
	}
	real = filepath.Join(real, suffix)
	for _, candidate := range []string{filepath.Clean(path), real} {
		for _, scratch := range []string{"/tmp", "/var/tmp"} {
			if candidate == scratch || strings.HasPrefix(candidate, scratch+"/") {
				return fmt.Errorf("public download root %s resolves into temporary storage; select a persistent volume", path)
			}
		}
	}
	return nil
}
