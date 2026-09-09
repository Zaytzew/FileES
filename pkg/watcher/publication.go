package watcher

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PublicationSnapshot describes observations, never the filesystem at recovery
// time. The commit owner persists it before mutation and acknowledges it only
// after establishing the exact remote result and local WC consistency.
type PublicationSnapshot struct {
	Session  string
	Sequence uint64
	Entries  []PublicationEntry
}

type PublicationEntry struct {
	Path     string
	Present  bool
	Mtime    int64
	Size     int64
	MD5      string
	Identity string
	IsDir    bool
}

func (s *Scanner) CapturePublication(paths []string) (*PublicationSnapshot, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &PublicationSnapshot{Session: s.session, Sequence: s.sequence}
	for _, path := range paths {
		if !publicationPath(path) {
			return nil, errors.New("invalid publication path")
		}
		m, ok := s.cur[path]
		p.Entries = append(p.Entries, PublicationEntry{path, ok, m.MtimeSec, m.Size, m.MD5, m.Identity, m.IsDir})
	}
	return p, nil
}

// AcknowledgePublication merges only selected observations into durable state.
// Unrelated new files and later edits must still be discovered after restart.
// A failed write leaves the owner's receipt unresolved for idempotent replay.
func (s *Scanner) AcknowledgePublication(p *PublicationSnapshot) error {
	if p == nil || p.Session == "" {
		return errors.New("missing publication observations")
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.workingCopyAvailable() {
		return errors.New("publication WC unavailable")
	}
	base := make(map[string]diskEntry)
	b, err := os.ReadFile(s.statePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		var list []diskEntry
		if err = json.Unmarshal(b, &list); err != nil {
			return err
		}
		for _, e := range list {
			base[e.Path] = e
		}
	}
	seen := make(map[string]bool)
	for _, e := range p.Entries {
		if !publicationPath(e.Path) || seen[e.Path] {
			return errors.New("invalid publication observations")
		}
		seen[e.Path] = true
		if e.Present {
			base[e.Path] = diskEntry{Path: e.Path, Mtime: e.Mtime, Size: e.Size, MD5: e.MD5, Identity: e.Identity, IsDir: e.IsDir}
		} else {
			delete(base, e.Path)
		}
	}
	list := make([]diskEntry, 0, len(base))
	for _, e := range base {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	if err = s.writeJSON(s.statePath, list); err != nil {
		return err
	}
	for _, e := range p.Entries {
		// A later completed scan is newer than this receipt. Never roll it back.
		if p.Session != s.session || s.sequence <= p.Sequence {
			delete(s.missingSince, e.Path)
			if e.Present {
				s.cur[e.Path] = meta{MtimeSec: e.Mtime, Size: e.Size, MD5: e.MD5, Identity: e.Identity, IsDir: e.IsDir}
			} else {
				delete(s.cur, e.Path)
			}
		}
		if p.Session == s.session && p.Sequence > s.acknowledged[e.Path] {
			s.acknowledged[e.Path] = p.Sequence
		}
	}
	s.totalBytes = indexBytes(s.cur)
	return nil
}

// EventAcknowledged rejects only queued observations included in this process's
// confirmed batch. Newer scans and all unselected paths remain actionable.
func (s *Scanner) EventAcknowledged(ev Event) bool {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	return ev.Session != "" && ev.Session == s.session && ev.Sequence > 0 && ev.Sequence <= s.acknowledged[ev.Rel]
}

func publicationPath(p string) bool {
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\:\x00\r\n") || filepath.IsAbs(p) || filepath.ToSlash(filepath.Clean(p)) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") {
			return false
		}
	}
	return true
}
