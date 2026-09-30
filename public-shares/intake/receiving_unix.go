//go:build !windows

package intake

import (
	"errors"
	"io"
	"os"

	"filees/public-shares/storage"
	"golang.org/x/sys/unix"
)

func createReceivingLease(path string) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, jobFilePerm)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(jobFilePerm); err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Caller holds budget lock. Never invent a lease for a legacy directory.
// UUIDs are never reused; no new receiver can open an existing job's lock.
func (s Store) sweepReceiving() error {
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !canonicalID(entry.Name()) {
			continue
		}
		job, err := root.OpenRoot(entry.Name())
		if err != nil {
			return err
		}
		removed, err := reapReceiving(job)
		job.Close()
		if err != nil {
			return err
		}
		if removed {
			if err := root.Remove(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func reapReceiving(job *os.Root) (bool, error) {
	for _, name := range []string{readyName, "PROCESSING"} {
		if _, err := job.Lstat(name); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	info, err := job.Lstat(receivingLockName)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, ErrBudgetState
	}
	f, err := job.OpenFile(receivingLockName, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, ErrBudgetState
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	dir, err := job.Open(".")
	if err != nil {
		return false, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case receivingLockName, reservationName, payloadName, ".payload.tmp", metaName, ".meta.json.tmp":
		default:
			return false, ErrBudgetState
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return false, ErrBudgetState
		}
	}
	// Validate the complete flat layout before removing any part of it.
	for _, entry := range entries {
		if entry.Name() == receivingLockName {
			continue
		}
		if _, err := storage.RemoveRegular(job, entry.Name()); err != nil {
			return false, err
		}
	}
	_, err = storage.RemoveRegular(job, receivingLockName)
	return err == nil, err
}
