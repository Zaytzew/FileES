// Package androidbind is the gomobile-bindable facade over pkg/mobileclient.
// gomobile bind cannot cross exported types containing json.RawMessage or
// map[string]string (both appear in pkg/mobile/v1), so this facade exposes
// only string/bool/error at the boundary; Kotlin never sees the v1 types.
package androidbind

import (
	"encoding/json"

	v1 "filees/pkg/mobile/v1"
	"filees/pkg/mobileclient"
)

// Store is the gomobile-bindable handle onto the local manifest cache under
// root. It never talks to the network; it is the read side a Kotlin
// ContentProvider can use directly while offline or before a Refresh.
type Store struct {
	inner mobileclient.Store
}

// NewStore opens the local store rooted at dir (the app's non-evictable
// filesDir on Android).
func NewStore(dir string) *Store {
	return &Store{inner: mobileclient.Store{Root: dir}}
}

// LoadManifestJSON returns the cached manifest for repoID as JSON, or ""
// with a nil error if nothing is cached yet.
func (s *Store) LoadManifestJSON(repoID string) (string, error) {
	m, err := s.inner.LoadManifest(repoID)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// SaveManifestJSONIfNewer decodes manifestJSON and persists it if it is newer
// than the cache along the monotonic (view_generation, repo_revision)
// ordering. It reports whether the cache was written.
func (s *Store) SaveManifestJSONIfNewer(manifestJSON string) (bool, error) {
	var m v1.Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &m); err != nil {
		return false, err
	}
	return s.inner.SaveManifestIfNewer(&m)
}
