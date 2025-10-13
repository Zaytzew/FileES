package watcher

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"filees/pkg/talk"
)

// EventType enumerates filesystem changes detected by the scanner.
type EventType int

const (
	FileAdded EventType = iota
	FileModified
	FileDeleted
)

func (et EventType) String() string {
	switch et {
	case FileAdded:
		return "FileAdded"
	case FileModified:
		return "FileModified"
	case FileDeleted:
		return "FileDeleted"
	default:
		return "Unknown"
	}
}

// Event is sent on the event channel for each detected change.
type Event struct {
	Type   EventType   `json:"type"`
	Path   string      `json:"path"`   // absolute path (for convenience)
	Rel    string      `json:"rel"`    // path relative to WC root
	MD5    string      `json:"md5"`    // checksum at the moment of the event (when available)
	Size   int64       `json:"size"`
	Mtime  time.Time   `json:"mtime"`
}

// fileMeta is kept in the on-disk manifest.
type fileMeta struct {
	MD5   string    `json:"md5"`
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
}

type manifest struct {
	Version int                 `json:"version"`
	Root    string              `json:"root"`
	Files   map[string]fileMeta `json:"files"` // key: rel path
}

// Options define scanner configuration expected by main.go.
type Options struct {
	WC         string        // working copy root (must exist)
	StatePath  string        // path to manifest.json (optional, but recommended)
	ScanPeriod time.Duration // how often to rescan
	LogScope   string        // talk scope (e.g. "watch:<repoID>")
	UseMD5     bool          // default true; when false, detect by mtime/size only
	ChanSize   int           // size of output channel buffer (default 1024)
}

// Scanner performs periodic full scans (shell-first, no inotify).
type Scanner struct {
	wc         string
	statePath  string
	period     time.Duration
	useMD5     bool
	lg         talk.Logger

	mu    sync.Mutex
	files map[string]fileMeta // rel path -> meta
}

// NewScanner constructs a scanner and (best-effort) loads manifest if StatePath is provided.
func NewScanner(opts Options) (Scanner, error) {
	if opts.WC == "" {
		return Scanner{}, errors.New("watcher: Options.WC is empty")
	}
	info, err := os.Stat(opts.WC)
	if err != nil || !info.IsDir() {
		return Scanner{}, errors.New("watcher: working copy path does not exist or is not a directory")
	}
	if opts.ScanPeriod <= 0 {
		opts.ScanPeriod = 15 * time.Second
	}
	if opts.ChanSize <= 0 {
		opts.ChanSize = 1024
	}
	useMD5 := opts.UseMD5
	if !opts.UseMD5 { /* explicit */ } else { useMD5 = true }

	s := Scanner{
		wc:        filepath.Clean(opts.WC),
		statePath: opts.StatePath,
		period:    opts.ScanPeriod,
		useMD5:    useMD5,
		lg:        talk.With(opts.LogScope),
		files:     make(map[string]fileMeta),
	}
	// best-effort auto load
	if opts.StatePath != "" {
		_ = s.LoadState(opts.StatePath)
	}
	return s, nil
}

// Start begins the periodic scan loop. It returns a receive-only channel of events.
// The channel is closed when ctx is done.
func (s *Scanner) Start(ctx context.Context) <-chan Event {
	events := make(chan Event, 1024)
	if s.period > 0 && s.period != 15*time.Second { /* keep */ }

	go func() {
		defer close(events)
		t := time.NewTicker(s.period)
		defer t.Stop()

		// initial scan immediately
		s.scanOnce(ctx, events)

		for {
			select {
			case <-ctx.Done():
				// best-effort autosave
				if s.statePath != "" { _ = s.SaveState(s.statePath) }
				return
			case <-t.C:
				s.scanOnce(ctx, events)
			}
		}
	}()
	return events
}

// LoadState loads manifest from a given path.
func (s *Scanner) LoadState(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil { return err }
	if m.Files == nil { m.Files = make(map[string]fileMeta) }
	s.files = m.Files
	return nil
}

// SaveState persists the current manifest to the given path.
func (s *Scanner) SaveState(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := manifest{Version: 1, Root: s.wc, Files: s.files}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil { return err }
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, data, 0o644)
}

// scanOnce walks the tree and emits events by comparing current snapshot with stored manifest.
func (s *Scanner) scanOnce(ctx context.Context, out chan<- Event) {
	s.lg.Tracef("scan start: %s", s.wc)

	curr := make(map[string]fileMeta)
	deleted := make(map[string]fileMeta)

	s.mu.Lock()
	for p, meta := range s.files { deleted[p] = meta }
	s.mu.Unlock()

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil { s.lg.Warnf("walk error: %s: %v", path, err); return nil }

		name := d.Name()
		// Skip meta dirs and VCS internals
		if d.IsDir() {
			if name == ".svn" || name == ".filees" { return fs.SkipDir }
			return nil
		}
		// Skip symlinks (files)
		if d.Type()&os.ModeSymlink != 0 { return nil }

		// Validate path elements (simple sanity)
		if !isValidRel(path, s.wc) { return nil }

		// gather info
		info, ierr := d.Info()
		if ierr != nil { s.lg.Warnf("stat error: %s: %v", path, ierr); return nil }

		rel, _ := filepath.Rel(s.wc, path)
		old, had := s.files[rel]

		meta := fileMeta{ Size: info.Size(), Mtime: info.ModTime() }
		// checksum only when necessary
		if s.useMD5 {
			if !had || old.Size != meta.Size || !timeEqual(old.Mtime, meta.Mtime) {
				sum, herr := md5File(path)
				if herr != nil { s.lg.Warnf("md5 error: %s: %v", path, herr); return nil }
				meta.MD5 = sum
			} else {
				meta.MD5 = old.MD5
			}
		}

		curr[rel] = meta
		// Decide events
		abs := path
		if runtime.GOOS == "windows" { abs = filepath.Clean(path) }

		if !had {
			out <- Event{Type: FileAdded, Path: abs, Rel: rel, MD5: meta.MD5, Size: meta.Size, Mtime: meta.Mtime}
		} else {
			changed := (meta.Size != old.Size) || !timeEqual(meta.Mtime, old.Mtime)
			if s.useMD5 { changed = changed || (meta.MD5 != old.MD5) }
			if changed { out <- Event{Type: FileModified, Path: abs, Rel: rel, MD5: meta.MD5, Size: meta.Size, Mtime: meta.Mtime} }
			delete(deleted, rel)
		}
		return nil
	}

	// WalkDir is more efficient than Walk
	_ = filepath.WalkDir(s.wc, walkFn)

	// emit deletions
	for rel, meta := range deleted {
		abs := filepath.Join(s.wc, rel)
		out <- Event{Type: FileDeleted, Path: abs, Rel: rel, MD5: meta.MD5, Size: meta.Size, Mtime: meta.Mtime}
	}

	// swap manifest
	s.mu.Lock()
	s.files = curr
	s.mu.Unlock()

	s.lg.Tracef("scan end: %s", s.wc)
}

func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil { return "", err }
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil { return "", err }
	return hex.EncodeToString(h.Sum(nil)), nil
}

func timeEqual(a, b time.Time) bool {
	// Handle different FS timestamp resolutions by rounding to 1ms
	const res = time.Millisecond
	return a.Round(res).Equal(b.Round(res))
}

func isValidRel(absPath, root string) bool {
	rel, err := filepath.Rel(root, absPath)
	if err != nil { return false }
	if strings.HasPrefix(rel, "..") { return false }
	// guard against odd control chars in path parts
	for _, part := range splitPath(rel) {
		for _, r := range part {
			if r < 0x20 { return false }
		}
		if part == "." || part == ".." { return false }
	}
	return true
}

func splitPath(p string) []string {
	parts := []string{}
	for {
		dir, file := filepath.Split(p)
		if file != "" { parts = append([]string{file}, parts...) }
		if dir == "" || dir == "/" || dir == "." { break }
		p = strings.TrimSuffix(dir, string(os.PathSeparator))
	}
	return parts
}

