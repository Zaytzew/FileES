package reservationclient

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
	"filees/pkg/privatefile"
	v1 "filees/pkg/reservation/v1"
)

// A latch remembers risk, not health. Absence leaves existing server checks
// in charge; neither elapsed time nor a failed request clears a known block.
// The coordinator serializes reads/updates for this profile.
func storageLatchPath(cache, server, repo string) (string, error) {
	if !filepath.IsAbs(cache) || server == "" || repo == "" {
		return "", errors.New("invalid storage latch binding")
	}
	id := sha256.Sum256([]byte(server + "\x00" + repo))
	return filepath.Join(filepath.Dir(cache), "storage-holds", fmt.Sprintf("%x.hold", id)), nil
}

func StorageBlocked(cache, server, repo string) (bool, error) {
	p, err := storageLatchPath(cache, server, repo)
	if err != nil {
		return true, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return true, err
	}
	if string(raw) != "filees-storage-hold/v1\n" {
		return true, errors.New("invalid storage hold record")
	}
	return true, nil
}

// Persist immediately on receipt, including while there is no pending batch.
// Whole request elapsed time is subtracted conservatively from server lifetime.
func ObserveStorage(cache, server, repo string, result v1.Result, elapsed time.Duration) error {
	s := result.StorageWrite
	if s == nil || s.State == "unknown" {
		return nil
	}
	if result.RepoID != repo || result.RepositoryState != "active" || (result.Schema != v1.StateSchema && result.Schema != v1.AutolockSchema) {
		return errors.New("storage observation binding mismatch")
	}
	p, err := storageLatchPath(cache, server, repo)
	if err != nil {
		return err
	}
	if s.State == "available" {
		if elapsed < 0 || s.ValidForSeconds < 1 || s.ValidForSeconds > 180 || s.MeasuredAt == nil || s.MeasuredAt.IsZero() || elapsed >= time.Duration(s.ValidForSeconds)*time.Second {
			return nil
		}
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		return durable.SyncDirectory(filepath.Dir(p))
	}
	if s.State != "blocked" {
		return errors.New("invalid storage observation state")
	}
	if blocked, err := StorageBlocked(cache, server, repo); blocked && err == nil {
		return nil
	}
	if err := privatefile.EnsureDir(filepath.Dir(p)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".hold-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("filees-storage-hold/v1\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := privatefile.Harden(f.Name()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(p))
}
