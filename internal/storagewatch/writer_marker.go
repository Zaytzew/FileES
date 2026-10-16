package storagewatch

import (
	"errors"
	"os"
	"path/filepath"

	"filees/internal/durable"
)

var ErrUnfinishedWriter = errors.New("capacity writer did not acknowledge completion; administrator inspection required")

func (g Gate) writerDirectory() string {
	return filepath.Join(g.Root, "capacity-active-writers")
}

// A marker precedes operation effects and survives abrupt parent death. It
// does not contain client data, a PID or a secret. The monitor never infers
// completion from age/PID, nor deletes a marker to manufacture acknowledgement.
func (g Gate) beginWriter() (string, error) {
	dir := g.writerDirectory()
	if err := EnsurePrivateDirectory(dir, g.Root); err != nil {
		return "", err
	}
	if err := EnsurePrivateDirectory(g.observationDirectory(), g.Root); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "writer-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	failed := func(err error) (string, error) {
		return "", errors.Join(err, f.Close(), os.Remove(path))
	}
	if err := preservePolicyOwner(f, dir); err != nil {
		return failed(err)
	}
	if _, err := f.WriteString("filees.capacity-writer/v1\n"); err != nil {
		return failed(err)
	}
	if err := f.Sync(); err != nil {
		return failed(err)
	}
	if err := f.Close(); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	if err := durable.SyncDirectory(dir); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	return path, nil
}

func (g Gate) finishWriter(path string) error {
	if filepath.Dir(path) != g.writerDirectory() {
		return ErrUnfinishedWriter
	}
	// Persist invalidation even when the operation failed after partial
	// effects. Never remove its recovery marker if this acknowledgement fails.
	if err := g.advanceObservationEpoch(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return durable.SyncDirectory(g.writerDirectory())
}

// The exclusive lease proves all cooperative parents are done. Persistent
// markers additionally prevent an orphan child from becoming a false success.
// An unknown entry fails closed too; recovery is an operator decision.
func (g Gate) requireFinishedWriters() error {
	dir := g.writerDirectory()
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // legacy server, before any admitted guarded operation
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privatePolicyMode(info.Mode()) {
		return ErrUnfinishedWriter
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return ErrUnfinishedWriter
	}
	return nil
}
