// Package reponames keeps the names this desktop shows for repositories.
//
// A name here is presentation only: it never reaches the daemon, the server
// or the working copy on disk. The repository keeps the name it was created
// with everywhere else; this client just shows another one in its place.
package reponames

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"filees/internal/durable"
	"filees/pkg/privatefile"
)

const Schema = "filees.gui-repo-names/v1"

// MaxRunes bounds a shown name; a longer one is a mistake, not a name.
const MaxRunes = 120

var ErrInvalidName = errors.New("repository name must be one line of at most 120 characters")

type entry struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Name     string `json:"name"`
}

type document struct {
	Schema string  `json:"schema"`
	Names  []entry `json:"names"`
}

// Store holds the names in one file. FileES runs a single GUI instance, so
// the in-process lock is the only one needed.
type Store struct {
	path  string
	mu    sync.RWMutex
	names map[string]string
}

func key(serverID, repoID string) string { return serverID + "\x00" + repoID }

// Open reads path if it exists. A missing file is an empty store; a damaged
// one is reported and treated as empty, because losing a shown name must never
// keep the client from starting.
func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("repository names path must be absolute")
	}
	store := &Store{path: filepath.Clean(path), names: map[string]string{}}
	raw, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return store, fmt.Errorf("read repository names: %w", err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Schema != Schema {
		return store, fmt.Errorf("repository names file %s is not %s", store.path, Schema)
	}
	for _, item := range doc.Names {
		if name, ok := Normalize(item.Name); ok && name != "" && item.ServerID != "" && item.RepoID != "" {
			store.names[key(item.ServerID, item.RepoID)] = name
		}
	}
	return store, nil
}

// Normalize trims a name and checks it is one printable line. An empty result
// means "show the repository's own name again".
func Normalize(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxRunes {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

// Name returns the name this client shows for a repository, if one was set.
func (s *Store) Name(serverID, repoID string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, ok := s.names[key(serverID, repoID)]
	return name, ok
}

// Set stores a name; an empty name removes it. The file is replaced
// atomically, and the memory copy changes only after the file did.
func (s *Store) Set(serverID, repoID, name string) error {
	if s == nil {
		return errors.New("repository names unavailable")
	}
	serverID, repoID = strings.TrimSpace(serverID), strings.TrimSpace(repoID)
	if serverID == "" || repoID == "" {
		return errors.New("repository names need a server and a repository")
	}
	name, ok := Normalize(name)
	if !ok {
		return ErrInvalidName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]string, len(s.names)+1)
	for k, v := range s.names {
		next[k] = v
	}
	if name == "" {
		delete(next, key(serverID, repoID))
	} else {
		next[key(serverID, repoID)] = name
	}
	if err := s.write(next); err != nil {
		return err
	}
	s.names = next
	return nil
}

func (s *Store) write(names map[string]string) error {
	doc := document{Schema: Schema, Names: make([]entry, 0, len(names))}
	for k, name := range names {
		serverID, repoID, _ := strings.Cut(k, "\x00")
		doc.Names = append(doc.Names, entry{ServerID: serverID, RepoID: repoID, Name: name})
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	root := filepath.Dir(s.path)
	if err := privatefile.EnsureDir(root); err != nil {
		return fmt.Errorf("prepare repository names: %w", err)
	}
	temporary, err := os.CreateTemp(root, ".repo-names-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := privatefile.Harden(temporaryPath); err != nil {
		return err
	}
	var renameErr error
	for attempt := 0; attempt < 20; attempt++ {
		if renameErr = os.Rename(temporaryPath, s.path); renameErr == nil {
			return durable.SyncDirectory(root)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return renameErr
}
