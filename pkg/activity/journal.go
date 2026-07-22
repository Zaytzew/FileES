// Package activity owns the daemon-side, durable recent synchronization feed.
package activity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const Schema = "filees.activity/v1"
const DefaultLimit = 20

type Kind string
type Stage string

const (
	Added    Kind = "added"
	Modified Kind = "modified"
	Deleted  Kind = "deleted"
	Renamed  Kind = "renamed"

	Detected   Stage = "detected"
	Pending    Stage = "pending"
	Publishing Stage = "publishing"
	Published  Stage = "published"
	Failed     Stage = "failed"
)

type Entry struct {
	RepoID     string    `json:"repo_id"`
	Path       string    `json:"path"`
	Kind       Kind      `json:"kind"`
	Stage      Stage     `json:"stage"`
	DetectedAt time.Time `json:"detected_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Revision   int64     `json:"revision,omitempty"`
	ErrorID    string    `json:"error_id,omitempty"`
}

type document struct {
	Schema  string  `json:"schema"`
	Entries []Entry `json:"entries"`
}

type Journal struct {
	mu      sync.Mutex
	path    string
	limit   int
	now     func() time.Time
	entries map[string]Entry
}

func Open(path string, limit int) (*Journal, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("activity journal path must be absolute")
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	j := &Journal{path: filepath.Clean(path), limit: limit, now: time.Now, entries: make(map[string]Entry)}
	raw, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Schema != Schema {
		return nil, errors.New("activity journal is invalid")
	}
	for _, entry := range doc.Entries {
		if err := validate(entry); err != nil {
			return nil, err
		}
		j.entries[key(entry.RepoID, entry.Path)] = entry
	}
	j.trim()
	return j, nil
}

// Record upserts one path. Successive pipeline stages replace the previous
// row, so the tray never presents the same object as both pending and done.
func (j *Journal) Record(entry Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now().UTC()
	k := key(entry.RepoID, entry.Path)
	if old, ok := j.entries[k]; ok {
		if entry.DetectedAt.IsZero() {
			entry.DetectedAt = old.DetectedAt
		}
		if entry.Kind == "" {
			entry.Kind = old.Kind
		}
	}
	if entry.DetectedAt.IsZero() {
		entry.DetectedAt = now
	}
	entry.UpdatedAt = now
	if err := validate(entry); err != nil {
		return err
	}
	j.entries[k] = entry
	j.trim()
	return j.persist()
}

func (j *Journal) List() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.sorted()
}

// Forget removes the entry for repoID/path, if any. Used when a locally
// staged operation is cancelled before ever reaching the repository (e.g. a
// file added and deleted again before its first commit, or a delete for a
// path that was never actually under version control) -- there is nothing
// meaningful to report as Published or Failed for something that never
// really happened, and leaving the last-known stage in place would hang
// forever since nothing else will ever touch that path again.
func (j *Journal) Forget(repoID, path string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	k := key(repoID, path)
	if _, ok := j.entries[k]; !ok {
		return nil
	}
	delete(j.entries, k)
	return j.persist()
}

func (j *Journal) sorted() []Entry {
	out := make([]Entry, 0, len(j.entries))
	for _, entry := range j.entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].UpdatedAt.Equal(out[b].UpdatedAt) {
			return key(out[a].RepoID, out[a].Path) < key(out[b].RepoID, out[b].Path)
		}
		return out[a].UpdatedAt.After(out[b].UpdatedAt)
	})
	return out
}

func (j *Journal) trim() {
	ordered := j.sorted()
	if len(ordered) <= j.limit {
		return
	}
	for _, entry := range ordered[j.limit:] {
		delete(j.entries, key(entry.RepoID, entry.Path))
	}
}

func (j *Journal) persist() error {
	if err := os.MkdirAll(filepath.Dir(j.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(document{Schema: Schema, Entries: j.sorted()}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(j.path), ".activity-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(j.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validate(entry Entry) error {
	if strings.TrimSpace(entry.RepoID) == "" || entry.Path == "" || filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) == "." || strings.HasPrefix(filepath.Clean(entry.Path), ".."+string(filepath.Separator)) {
		return errors.New("activity repository and relative path are required")
	}
	switch entry.Kind {
	case Added, Modified, Deleted, Renamed:
	default:
		return errors.New("activity kind is invalid")
	}
	switch entry.Stage {
	case Detected, Pending, Publishing:
		if entry.Revision != 0 || entry.ErrorID != "" {
			return errors.New("unfinished activity cannot have a result")
		}
	case Published:
		if entry.Revision <= 0 || entry.ErrorID != "" {
			return errors.New("published activity requires a revision")
		}
	case Failed:
		if strings.TrimSpace(entry.ErrorID) == "" || entry.Revision != 0 {
			return errors.New("failed activity requires an error ID")
		}
	default:
		return errors.New("activity stage is invalid")
	}
	if entry.DetectedAt.IsZero() || entry.UpdatedAt.Before(entry.DetectedAt) {
		return errors.New("activity timestamps are invalid")
	}
	return nil
}

func key(repoID, path string) string { return repoID + "\x00" + filepath.ToSlash(filepath.Clean(path)) }
