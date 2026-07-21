// Package mobileclient is the transport-neutral Go core of the FileES Android
// client. It owns the local store, manifest reconciliation, the pending upload
// queue and the client-side protocol; the Kotlin shell provides platform APIs
// (ContentProvider, WorkManager, Keystore, camera) over a thin gomobile binding.
package mobileclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	v1 "filees/pkg/mobile/v1"
)

// Store is the local, durable client state under Root. On Android this is the
// app's non-evictable filesDir (never cacheDir): a lost pending upload is lost
// data.
type Store struct {
	Root string
}

func (s Store) manifestPath(repoID string) string {
	return filepath.Join(s.Root, "manifests", repoID+".json")
}

// LoadManifest returns the cached manifest for repoID, or (nil, nil) if none.
func (s Store) LoadManifest(repoID string) (*v1.Manifest, error) {
	raw, err := os.ReadFile(s.manifestPath(repoID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m v1.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode cached manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// SaveManifestIfNewer writes m only if it is newer than the cache along the
// monotonic (view_generation, repo_revision) ordering — both are monotonic
// server-side. Equal is an idempotent no-op; an older manifest is a rejected
// rollback, so a replayed or stale projection can never roll the client back.
func (s Store) SaveManifestIfNewer(m *v1.Manifest) (bool, error) {
	if err := m.Validate(); err != nil {
		return false, err
	}
	cur, err := s.LoadManifest(m.RepoID)
	if err != nil {
		return false, err
	}
	if cur != nil {
		if m.ViewGeneration < cur.ViewGeneration || m.RepoRevision < cur.RepoRevision {
			return false, errors.New("manifest rollback rejected")
		}
		if m.ViewGeneration == cur.ViewGeneration && m.RepoRevision == cur.RepoRevision {
			return false, nil
		}
	}
	if err := atomicWriteJSON(s.manifestPath(m.RepoID), m); err != nil {
		return false, err
	}
	return true, nil
}

// atomicWriteJSON writes value as indented JSON to path atomically, mode 0600.
func atomicWriteJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBytes(path, append(raw, '\n'))
}

// atomicWriteBytes writes data to path via temp file, fsync, rename, dir fsync,
// mode 0600.
func atomicWriteBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
