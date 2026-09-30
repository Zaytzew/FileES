package uploadworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"filees/public-shares/storage"
)

var ErrTrashFull = errors.New("upload rejection storage has reached its payload size limit; intake retained")
var ErrTrashState = errors.New("upload rejection storage usage cannot be established; intake retained")

// All writers and TTL/read operations share the same kernel lock. It survives
// process crashes without a time-based guess about whether a writer is alive.
func (r Reaper) ownTrash() (io.Closer, error) {
	if !filepath.IsAbs(r.TrashRoot) || r.MaxTrashBytes < 0 {
		return nil, ErrIncomplete
	}
	if err := os.MkdirAll(r.TrashRoot, 0700); err != nil {
		return nil, err
	}
	return ownTrashRoot(r.TrashRoot)
}

// Logical payload bytes, including incomplete copies. Hidden entries still
// count. Metadata is not charged; this is not a filesystem quota.
func (r Reaper) checkTrashBudget(ctx context.Context, incoming int64) error {
	if incoming < 0 {
		return ErrIncomplete
	}
	var used int64
	err := filepath.WalkDir(r.TrashRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe rejection storage entry: %s", path)
		}
		switch entry.Name() {
		case indexName, purgedLogName, ".maintenance.lock":
			return nil
		}
		// Unknown regular files are occupied bytes, not free space.
		if info.Size() < 0 || info.Size() > math.MaxInt64-used {
			return errors.New("rejection storage usage overflow")
		}
		used += info.Size()
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTrashState, err)
	}
	if r.MaxTrashBytes > 0 && (used > r.MaxTrashBytes || incoming > r.MaxTrashBytes-used) {
		return ErrTrashFull
	}
	return nil
}

// An expired entry is removed only if its directory contains exactly the
// recognised files. Unknown data and symlinks require administrator inspection.
func removeExpiredEntry(dir string, recordPurge func() error) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != payloadName && entry.Name() != indexName && entry.Name() != ".payload.tmp" {
			return errors.New("unknown file in expired rejection entry")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("unsafe file in expired rejection entry")
		}
	}
	// Do not grow the purge log for entries whose contents cannot be removed.
	if err := recordPurge(); err != nil {
		return err
	}
	// Leave the index until all payloads are gone, so a failed sweep is retryable.
	for _, name := range []string{payloadName, ".payload.tmp", indexName} {
		if _, err := storage.RemoveRegular(root, name); err != nil {
			return err
		}
	}
	return nil
}
