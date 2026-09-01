package watcher

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/pkg/filepolicy"
	"filees/pkg/talk"
)

// --- Public API ---

type EntryType string

const (
	EntryFile EntryType = "file"
	EntryDir  EntryType = "dir"
)

type OpType int

const (
	Added OpType = iota
	Modified
	Deleted
	Renamed // OS-level rename detected via MD5; OldRel = source path
)

// Event sent to commit.Service
// Path = ABS path (for stat/IO), Rel = POSIX path relative to WC (for SVN)
type Event struct {
	Path   string    // absolute path (new location for Renamed)
	Rel    string    // posix relative path (new location for Renamed)
	OldRel string    // source path for Renamed; empty otherwise
	Type   EntryType // file | dir
	Op     OpType    // Added | Modified | Deleted | Renamed
}

// Options control scanner behaviour
// Times/intervals default to sane values if zero.
type Options struct {
	WC              string         // ABS root of working copy (must exist)
	StatePath       string         // .filees/state/manifest.json
	ScanPeriod      time.Duration  // typical: commit window / 2 (default 15s)
	BusyPath        string         // .filees/state/commit.busy
	BusyTTL         time.Duration  // default 10m (stale busy ignore)
	TicketsPoll     time.Duration  // poll tickets-only while busy (default 12s)
	DeletedDebounce time.Duration  // emit FileDeleted after this absence (default 10m)
	IgnoreRegex     *regexp.Regexp // optional global regex (extra filter)
	LogScope        string

	// MD5 / hashing heuristics
	UseMD5           bool    // default true (tri-state semantics)
	MD5PerFileCutoff int64   // bytes; default 64*MiB
	MD5BudgetBytes   int64   // per scan; default 256*MiB
	MD5BudgetFrac    float64 // fraction of ScanPeriod time; default 0.30

	// Event channel buffer size
	ChanSize int // default 1024
	// RequireSVNMetadata prevents daemon housekeeping from recreating an old
	// working-copy root after the user moves it elsewhere.
	RequireSVNMetadata bool
}

// Scanner performs shell-first periodic scans
// BASELINING => PROMOTE => ACTIVE (per baseline.ok flag)
type Scanner struct {
	// static
	wc         string
	statePath  string
	period     time.Duration
	busyPath   string
	busyTTL    time.Duration
	ticketsInt time.Duration
	debounceD  time.Duration
	lg         talk.Logger

	useMD5             bool
	md5Cutoff          int64
	md5BudgetBytes     int64
	md5BudgetFrac      float64
	chanSize           int
	requireSVNMetadata bool

	// dynamic
	mu           sync.Mutex
	cur          index // current manifest snapshot (ACTIVE) or building (BASELINING)
	totalBytes   int64 // files represented by cur; excludes .svn and private .filees state
	sizeKnown    bool  // true after a manifest load or one complete tree scan
	missingSince map[string]time.Time

	// ignores (hot-reload user cfg)
	ignorePath   string // .filees/user_ignore.cfg
	ignoreMTime  time.Time
	builtinGlobs []glob // hardcoded defaults (office temps, OS noise, VCS dirs)
	hardGlobs    []glob
	softGlobs    []glob
	igRegex      *regexp.Regexp

	// md5 backlog persistence
	backlogPath   string // .filees/state/md5.backlog.json
	backlog       []backlogItem
	backlogSaveMu sync.Mutex // serialises concurrent saveBacklog calls

	// MD5s computed by backlog worker; applied to s.cur at next swap (avoids s.cur write race)
	pendingMD5 map[string]pendingMD5Entry

	// mode
	mode mode
}

type pendingMD5Entry struct {
	MD5   string
	Mtime int64
	Size  int64
}

type mode int

const (
	modeBaselining mode = iota
	modeActive
)

// index: RAM manifest map (rel POSIX path -> meta)
type index map[string]meta

type meta struct {
	MtimeSec int64  // epoch seconds
	Size     int64  // files only
	MD5      string // files only; may be ""
	IsDir    bool
}

// on-disk manifest: list of entries
// {path, mtime, [size], [md5]}
type diskEntry struct {
	Path  string `json:"path"`
	Mtime int64  `json:"mtime"`
	Size  int64  `json:"size,omitempty"`
	MD5   string `json:"md5,omitempty"`
}

// backlog persistence

type backlogItem struct {
	Rel      string `json:"rel"`
	Size     int64  `json:"size"`
	Mtime    int64  `json:"mtime"`
	QueuedAt int64  `json:"queued_at"`
}

// --- construction ---

func NewScanner(opts Options) (*Scanner, error) {
	if opts.WC == "" {
		return nil, errors.New("watcher: Options.WC is empty")
	}
	fi, err := os.Stat(opts.WC)
	if err != nil || !fi.IsDir() {
		return nil, errors.New("watcher: WC missing or not a directory")
	}

	if opts.ScanPeriod <= 0 {
		opts.ScanPeriod = 15 * time.Second
	}
	if opts.BusyTTL <= 0 {
		opts.BusyTTL = 10 * time.Minute
	}
	if opts.TicketsPoll <= 0 {
		opts.TicketsPoll = 12 * time.Second
	}
	if opts.DeletedDebounce <= 0 {
		opts.DeletedDebounce = 10 * time.Minute
	}
	if opts.ChanSize <= 0 {
		opts.ChanSize = 1024
	}
	if opts.MD5PerFileCutoff <= 0 {
		opts.MD5PerFileCutoff = 64 * 1024 * 1024
	}
	if opts.MD5BudgetBytes <= 0 {
		opts.MD5BudgetBytes = 256 * 1024 * 1024
	}
	if opts.MD5BudgetFrac <= 0 {
		opts.MD5BudgetFrac = 0.30
	}
	if opts.UseMD5 == false { /* explicit */
	} else {
		opts.UseMD5 = true
	}

	wc := filepath.Clean(opts.WC)
	stateDir := filepath.Join(wc, ".filees", "state")
	ignorePath := filepath.Join(wc, ".filees", "user_ignore.cfg")
	backlogPath := filepath.Join(stateDir, "md5.backlog.json")

	s := Scanner{
		wc:                 wc,
		statePath:          opts.StatePath,
		period:             opts.ScanPeriod,
		busyPath:           coalesce(opts.BusyPath, filepath.Join(stateDir, "commit.busy")),
		busyTTL:            opts.BusyTTL,
		ticketsInt:         opts.TicketsPoll,
		debounceD:          opts.DeletedDebounce,
		lg:                 talk.With(opts.LogScope),
		useMD5:             opts.UseMD5,
		md5Cutoff:          opts.MD5PerFileCutoff,
		md5BudgetBytes:     opts.MD5BudgetBytes,
		md5BudgetFrac:      opts.MD5BudgetFrac,
		chanSize:           opts.ChanSize,
		requireSVNMetadata: opts.RequireSVNMetadata,
		cur:                make(index),
		missingSince:       make(map[string]time.Time),
		ignorePath:         ignorePath,
		backlogPath:        backlogPath,
		pendingMD5:         make(map[string]pendingMD5Entry),
		igRegex:            opts.IgnoreRegex,
		mode:               modeBaselining,
	}

	// try load state to determine mode
	if s.exists(filepath.Join(wc, ".filees", "state", "baseline.ok")) {
		// baseline requested but not promoted yet -> still baselining; commit.Service will drop the flag after promote
	}
	// if manifest.json exists -> ACTIVE
	if s.exists(opts.StatePath) {
		if err := s.LoadState(opts.StatePath); err == nil {
			s.mode = modeActive
		}
	}
	// initialize builtin ignore globs
	s.builtinGlobs = make([]glob, len(filepolicy.BuiltinIgnorePatterns))
	for i, p := range filepolicy.BuiltinIgnorePatterns {
		s.builtinGlobs[i] = glob{raw: p}
	}

	// load ignores and backlog (best-effort)
	_ = s.reloadIgnores()
	_ = s.loadBacklog()
	return &s, nil
}

// Start scanning; returns read-only event channel
func (s *Scanner) Start(ctx context.Context) <-chan Event {
	events := make(chan Event, s.chanSize)
	backlogDone := make(chan struct{})
	go func() {
		defer close(backlogDone)
		s.runBacklogWorker(ctx)
	}()
	go func() {
		s.loop(ctx, events)
		<-backlogDone
		close(events)
	}()
	return events
}

// LoadState reads manifest.json (list) and builds in-memory map
func (s *Scanner) LoadState(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var list []diskEntry
	if err := dec.Decode(&list); err != nil {
		return err
	}
	m := make(index, len(list))
	for _, e := range list {
		isDir := strings.HasSuffix(e.Path, "/")
		m[e.Path] = meta{MtimeSec: e.Mtime, Size: e.Size, MD5: e.MD5, IsDir: isDir}
	}
	s.cur = m
	s.totalBytes = indexBytes(m)
	s.sizeKnown = true
	return nil
}

// WorkingCopySize returns the size already held by the watcher's in-memory
// manifest. It never walks the filesystem and deliberately follows the same
// ignore/private-state rules as change detection.
func (s *Scanner) WorkingCopySize() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes, s.sizeKnown
}

// SaveState writes the current map as a sorted list to path (atomically)
func (s *Scanner) SaveState(path string) error {
	if !s.workingCopyAvailable() {
		return errors.New("watcher: working copy metadata is missing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]diskEntry, 0, len(s.cur))
	for rel, m := range s.cur {
		de := diskEntry{Path: rel, Mtime: m.MtimeSec}
		if !m.IsDir {
			de.Size = m.Size
			if m.MD5 != "" {
				de.MD5 = m.MD5
			}
		}
		list = append(list, de)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	if err := s.writeJSON(path, list); err != nil {
		return err
	}
	return nil
}

// --- main loop ---

func (s *Scanner) loop(ctx context.Context, out chan<- Event) {
	ticker := time.NewTicker(s.period)
	defer ticker.Stop()

	// initial pass
	s.scanCycle(ctx, out)

	for {
		select {
		case <-ctx.Done():
			// Final synchronous scan closes the shutdown race: changes created
			// after the last periodic scan must still reach commit service before
			// the event channel is closed and drained.
			s.scanCycle(context.Background(), out)
			if s.statePath != "" {
				_ = s.SaveState(s.statePath)
			}
			return
		case <-ticker.C:
			s.scanCycle(ctx, out)
		}
	}
}

func (s *Scanner) scanCycle(ctx context.Context, out chan<- Event) {
	if !s.workingCopyAvailable() {
		return
	}
	start := time.Now()
	var aCnt, mCnt, dCnt, igCnt, md5Done, md5Skipped int
	var backlogLen int
	busy := s.isBusy()

	if s.mode == modeBaselining {
		// Build manifest.tmp silently
		_ = s.reloadIgnores()
		stateDir := filepath.Dir(s.statePath)
		tmpPath := filepath.Join(stateDir, "manifest.tmp")
		mp := s.scanTree(ctx, &aCnt, &mCnt, &dCnt, &igCnt, &md5Done, &md5Skipped /*emit=*/, false, out, busy)

		// write tmp
		_ = s.writeJSON(tmpPath, toDiskList(mp))

		// auto-promote: baseline is complete after the first successful scan
		if !s.requireSVNMetadata {
			_ = os.MkdirAll(filepath.Dir(s.statePath), 0o755)
		}
		if err := os.Rename(tmpPath, s.statePath); err == nil {
			_ = s.LoadState(s.statePath)
			s.mode = modeActive
			s.lg.Infof("baseline complete — switching to active mode (%d entries)", len(mp))
		}
	} else { // ACTIVE
		if busy {
			// tickets-only light scan
			s.scanTicketsOnly(out)
			// sleep/poll respecting ctx
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.ticketsInt):
				return
			}
		}
		_ = s.reloadIgnores()
		mp := s.scanTree(ctx, &aCnt, &mCnt, &dCnt, &igCnt, &md5Done, &md5Skipped /*emit=*/, true, out, false)
		// Swap in-memory state; apply worker MD5s that arrived during the scan walk.
		// backlogLen is snapped here so the log read doesn't race with the worker.
		s.mu.Lock()
		for rel, pe := range s.pendingMD5 {
			if m, ok := mp[rel]; ok && m.MtimeSec == pe.Mtime && m.Size == pe.Size {
				m.MD5 = pe.MD5
				mp[rel] = m
			}
			delete(s.pendingMD5, rel)
		}
		backlogLen = len(s.backlog)
		s.cur = mp
		s.totalBytes = indexBytes(mp)
		s.sizeKnown = true
		s.mu.Unlock()
		// persist backlog best-effort
		_ = s.saveBacklog()
	}

	if (aCnt + mCnt + dCnt) > 0 {
		dur := time.Since(start)
		s.lg.Infof("scan done in %s (A=%d M=%d D=%d) backlog=%d md5_done=%d md5_skipped=%d ignored=%d busy=%t",
			dur, aCnt, mCnt, dCnt, backlogLen, md5Done, md5Skipped, igCnt, busy)
	} else if isDebug() {
		dur := time.Since(start)
		s.lg.Debugf("scan done in %s (A=%d M=%d D=%d) backlog=%d md5_done=%d md5_skipped=%d ignored=%d busy=%t",
			dur, aCnt, mCnt, dCnt, backlogLen, md5Done, md5Skipped, igCnt, busy)
	}
}

func indexBytes(entries index) int64 {
	var total int64
	for _, entry := range entries {
		if !entry.IsDir && entry.Size > 0 {
			total += entry.Size
		}
	}
	return total
}

// scanTree walks the WC and returns a fresh index. If emit==true, it emits events based on diff vs current state.
func (s *Scanner) scanTree(ctx context.Context, aCnt, mCnt, dCnt, igCnt, md5Done, md5Skipped *int, emit bool, out chan<- Event, ticketsOnly bool) index {
	curr := make(index)
	deleted := make(map[string]meta)

	s.mu.Lock()
	for rel, m := range s.cur {
		deleted[rel] = m
	}
	s.mu.Unlock()

	md5BytesBudget := s.md5BudgetBytes
	timeBudget := time.Duration(float64(s.period) * s.md5BudgetFrac)
	budgetDeadline := time.Now().Add(timeBudget)

	type pendingAdd struct {
		absPath string
		rel     string
		isDir   bool
		m       meta
	}
	var newFiles []pendingAdd

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			s.lg.Warnf("walk error: %s: %v", path, err)
			return nil
		}
		// ignore any symlink (file or dir) — FS-0201: symlinks not supported
		if d.Type()&os.ModeSymlink != 0 {
			*igCnt++
			s.lg.Debugf("FS-0201: symlink skipped: %s", path)
			return nil
		}

		name := d.Name()
		if d.IsDir() {
			if name == ".svn" {
				return fs.SkipDir
			}
			// reconciliation conflict saves — never commit
			if strings.HasPrefix(name, "!kolizje") {
				*igCnt++
				return fs.SkipDir
			}
		}

		rel := s.toRelPOSIX(path)
		if rel == "" {
			return nil
		}

		// .filees is private runtime state, except for outgoing tickets which
		// intentionally participate in synchronization. Never let commit cache,
		// manifests, locks, backlog or ignore configuration enter the watcher
		// manifest and feed back into commit staging.
		if rel == ".filees" {
			*igCnt++
			return nil // traverse so .filees/tickets remains visible
		}
		isTicket := rel == ".filees/tickets" || strings.HasPrefix(rel, ".filees/tickets/")
		if strings.HasPrefix(rel, ".filees/") && !isTicket {
			if d.IsDir() {
				return fs.SkipDir
			}
			*igCnt++
			return nil
		}

		// user ignores (hard first)
		if s.isIgnored(rel, d.IsDir()) {
			*igCnt++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// gather info
		info, ierr := d.Info()
		if ierr != nil {
			s.lg.Warnf("stat error: %s: %v", path, ierr)
			return nil
		}
		isDir := info.IsDir()

		// record into curr snapshot
		m := meta{MtimeSec: info.ModTime().Unix(), IsDir: isDir}
		if !isDir {
			m.Size = info.Size()
		}
		curr[rel] = m

		old, had := deleted[rel] // use the locked snapshot; don't read s.cur without mu

		// decide events
		if emit {
			if !had {
				// compute MD5 for rename detection (best-effort, within budget)
				if s.useMD5 && !isDir && m.Size <= s.md5Cutoff && md5BytesBudget > 0 && time.Now().Before(budgetDeadline) {
					if sum, readB, herr := md5FileBudgeted(path, md5BytesBudget); herr == nil {
						m.MD5 = sum
						md5BytesBudget -= readB
						*md5Done++
						curr[rel] = m
					}
				}
				newFiles = append(newFiles, pendingAdd{pathAbs(path), rel, isDir, m})
			} else {
				if _, wasMissing := s.missingSince[rel]; wasMissing {
					// A path returning during deletion debounce must be announced
					// again. Its earlier Added event may already have been cancelled
					// by the commit layer when the path disappeared.
					if !isDir {
						m.MD5 = old.MD5
					}
					curr[rel] = m
					out <- Event{Path: pathAbs(path), Rel: rel, Type: pickType(isDir), Op: Added}
					*aCnt++
					delete(deleted, rel)
					delete(s.missingSince, rel)
					return nil
				}
				changed := false
				if !isDir {
					if m.Size != old.Size || m.MtimeSec != old.MtimeSec {
						if s.useMD5 {
							if m.Size <= s.md5Cutoff && md5BytesBudget > 0 && time.Now().Before(budgetDeadline) {
								sum, readB, herr := md5FileBudgeted(path, md5BytesBudget)
								if herr == nil {
									m.MD5 = sum
									md5BytesBudget -= readB
									*md5Done++
									// suppress event if content unchanged (same hash)
									if old.MD5 == "" || m.MD5 != old.MD5 {
										changed = true
									}
								} else {
									*md5Skipped++
									changed = true
								}
							} else {
								s.enqueueBacklog(rel, m.Size, m.MtimeSec)
								*md5Skipped++
								changed = true
							}
						} else {
							changed = true
						}
					} else {
						m.MD5 = old.MD5 // unchanged — preserve hash from previous scan
					}
					curr[rel] = m // update curr with any newly computed or preserved MD5
				}
				if changed {
					out <- Event{Path: pathAbs(path), Rel: rel, Type: EntryFile, Op: Modified}
					*mCnt++
				}
				delete(deleted, rel)
				delete(s.missingSince, rel) // file is present — reset debounce timer
			}
		} else {
			// baselining: pre-fill MD5 under budget
			if !isDir && s.useMD5 && m.Size <= s.md5Cutoff && md5BytesBudget > 0 && time.Now().Before(budgetDeadline) {
				sum, readB, herr := md5FileBudgeted(path, md5BytesBudget)
				if herr == nil {
					m.MD5 = sum
					md5BytesBudget -= readB
					*md5Done++
				} else {
					*md5Skipped++
				}
				curr[rel] = m
			} else if !isDir && s.useMD5 && m.Size > s.md5Cutoff {
				s.enqueueBacklog(rel, m.Size, m.MtimeSec)
			}
		}
		return nil
	}

	_ = filepath.WalkDir(s.wc, walkFn)

	// rename detection + Added/Renamed event emission (active mode only)
	if emit && len(newFiles) > 0 {
		// index deleted files by MD5 (files only, with known hash)
		oldByMD5 := make(map[string]string, len(deleted))
		for oldRel := range deleted {
			if om := s.cur[oldRel]; om.MD5 != "" && !om.IsDir {
				oldByMD5[om.MD5] = oldRel
			}
		}
		for _, nf := range newFiles {
			if !nf.isDir && nf.m.MD5 != "" {
				if oldRel, ok := oldByMD5[nf.m.MD5]; ok {
					out <- Event{Path: nf.absPath, Rel: nf.rel, OldRel: oldRel, Type: EntryFile, Op: Renamed}
					delete(deleted, oldRel)
					delete(s.missingSince, oldRel)
					*aCnt++
					continue
				}
			}
			out <- Event{Path: nf.absPath, Rel: nf.rel, Type: pickType(nf.isDir), Op: Added}
			*aCnt++
		}
	} else if !emit {
		// baselining emits nothing — newFiles collected but unused
	}

	// handle deletions (emit only if debounce exceeded; in baselining we emit nothing)
	if emit {
		now := time.Now()
		for rel, old := range deleted {
			first, seen := s.missingSince[rel]
			if !seen {
				s.missingSince[rel] = now
				curr[rel] = old // keep in manifest — debounce just started
				continue
			}
			if now.Sub(first) >= s.debounceD {
				out <- Event{Path: filepath.Join(s.wc, fromPOSIX(rel)), Rel: rel, Type: pickType(old.IsDir), Op: Deleted}
				*dCnt++
				delete(s.missingSince, rel)
				// don't add to curr — file is confirmed gone
			} else {
				curr[rel] = old // still in debounce — keep in manifest so next scan checks again
			}
		}
	} else {
		// baselining resets debounce map
		s.missingSince = make(map[string]time.Time)
	}

	return curr
}

// tickets-only quick scan while busy; emits Added only for ticket files not yet in manifest
func (s *Scanner) scanTicketsOnly(out chan<- Event) {
	root := filepath.Join(s.wc, ".filees", "tickets")
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel := s.toRelPOSIX(path)
		if rel == "" {
			return nil
		}
		if _, seen := cur[rel]; !seen {
			out <- Event{Path: pathAbs(path), Rel: rel, Type: EntryFile, Op: Added}
		}
		return nil
	})
}

// --- ignores ---

type glob struct{ raw string }

func (g glob) match(rel string, isDir bool) bool {
	p := g.raw
	if len(p) > 0 && p[0] == '!' {
		p = p[1:]
	}
	if !strings.Contains(p, "**") {
		ok, _ := filepath.Match(fromPOSIX(p), fromPOSIX(rel))
		return ok
	}
	return matchDoublestar(p, rel)
}

// matchDoublestar handles patterns containing "**".
// "**" matches zero or more path components.
// Examples: "**/*.blend" matches any .blend at any depth;
// "src/**/*.blend" matches any .blend under src/.
func matchDoublestar(pattern, rel string) bool {
	if pattern == "**" {
		return true
	}

	idx := strings.Index(pattern, "**/")
	if idx < 0 {
		// trailing "**" — e.g. "src/**"
		prefix := strings.TrimSuffix(pattern, "**")
		return strings.HasPrefix(rel, prefix)
	}

	prefix := pattern[:idx]   // e.g. "src/" or ""
	suffix := pattern[idx+3:] // e.g. "*.blend" or "file.txt"

	if prefix != "" {
		if !strings.HasPrefix(rel, prefix) {
			return false
		}
		rel = rel[len(prefix):]
	}

	// match suffix against every trailing sub-path of rel
	for {
		ok, _ := filepath.Match(fromPOSIX(suffix), fromPOSIX(rel))
		if ok {
			return true
		}
		i := strings.Index(rel, "/")
		if i < 0 {
			break
		}
		rel = rel[i+1:]
	}
	return false
}

func (s *Scanner) isIgnored(rel string, isDir bool) bool {
	if s.igRegex != nil && s.igRegex.MatchString(rel) {
		return true
	}
	for _, g := range s.builtinGlobs {
		if g.match(rel, isDir) {
			return true
		}
	}
	for _, g := range s.hardGlobs {
		if g.match(rel, isDir) {
			return true
		}
	}
	for _, g := range s.softGlobs {
		if g.match(rel, isDir) {
			return true
		}
	}
	return false
}

func (s *Scanner) reloadIgnores() error {
	fi, err := os.Stat(s.ignorePath)
	if err != nil {
		return nil
	}
	if !fi.ModTime().After(s.ignoreMTime) {
		return nil
	}
	f, err := os.Open(s.ignorePath)
	if err != nil {
		return err
	}
	defer f.Close()
	s.hardGlobs = nil
	s.softGlobs = nil
	s.ignoreMTime = fi.ModTime()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// normalize to POSIX
		line = toPOSIX(line)
		if strings.HasPrefix(line, "!") {
			s.hardGlobs = append(s.hardGlobs, glob{raw: line})
			continue
		}
		s.softGlobs = append(s.softGlobs, glob{raw: line})
	}
	return nil
}

// --- md5 helpers / backlog ---

func (s *Scanner) enqueueBacklog(rel string, size int64, mtime int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.backlog {
		if s.backlog[i].Rel == rel {
			if s.backlog[i].Size != size || s.backlog[i].Mtime != mtime {
				s.backlog[i].Size = size
				s.backlog[i].Mtime = mtime
				s.backlog[i].QueuedAt = time.Now().Unix()
			}
			return
		}
	}
	s.backlog = append(s.backlog, backlogItem{Rel: rel, Size: size, Mtime: mtime, QueuedAt: time.Now().Unix()})
	if len(s.backlog) > 1000 {
		s.backlog = s.backlog[len(s.backlog)-1000:]
	}
}

func (s *Scanner) loadBacklog() error {
	f, err := os.Open(s.backlogPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var list []backlogItem
	if err := dec.Decode(&list); err != nil {
		return nil
	}
	// prioritize: newer mtime first; then bigger files
	sort.Slice(list, func(i, j int) bool {
		if list[i].Mtime == list[j].Mtime {
			return list[i].Size > list[j].Size
		}
		return list[i].Mtime > list[j].Mtime
	})
	s.backlog = list
	return nil
}

func (s *Scanner) saveBacklog() error {
	if !s.workingCopyAvailable() {
		return errors.New("watcher: working copy metadata is missing")
	}
	// backlogSaveMu serialises concurrent calls (scanner + worker both call this).
	// Snapshot is taken inside the write lock so the last caller always writes the
	// freshest state — no older snapshot can overwrite a newer one.
	s.backlogSaveMu.Lock()
	defer s.backlogSaveMu.Unlock()
	s.mu.Lock()
	snap := make([]backlogItem, len(s.backlog))
	copy(snap, s.backlog)
	s.mu.Unlock()
	return s.writeJSON(s.backlogPath, snap)
}

func (s *Scanner) workingCopyAvailable() bool {
	if !s.requireSVNMetadata {
		return true
	}
	info, err := os.Stat(filepath.Join(s.wc, ".svn"))
	return err == nil && info.IsDir()
}

func (s *Scanner) writeJSON(path string, value any) error {
	if !s.requireSVNMetadata {
		return atomicWriteJSON(path, value)
	}
	// startReadWrite creates these directories before scanning. If the WC was
	// moved after the metadata check, CreateTemp fails instead of recreating it.
	return atomicWriteJSONInExistingDir(path, value)
}

// runBacklogWorker processes large-file MD5 entries in the background.
// It computes one MD5 per 5 s so it never saturates I/O during normal use.
func (s *Scanner) runBacklogWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processOneBacklogItem(ctx)
		}
	}
}

// processOneBacklogItem picks the smallest pending backlog entry, verifies the
// file is still stable, computes its MD5, then updates s.cur and removes the entry.
func (s *Scanner) processOneBacklogItem(ctx context.Context) {
	s.mu.Lock()
	if len(s.backlog) == 0 {
		s.mu.Unlock()
		return
	}

	// Pick smallest file — fastest to hash; also most likely a rename candidate
	idx := 0
	for i := 1; i < len(s.backlog); i++ {
		if s.backlog[i].Size < s.backlog[idx].Size {
			idx = i
		}
	}
	item := s.backlog[idx]
	s.mu.Unlock()

	absPath := filepath.Join(s.wc, filepath.FromSlash(item.Rel))
	fi, err := os.Stat(absPath)
	if err != nil || fi.Size() != item.Size || fi.ModTime().Unix() != item.Mtime {
		// File gone or changed since enqueue — drop only the matching entry.
		s.removeBacklogEntry(item)
		return
	}

	// Use size+1 as budget so the whole file is read (budget==size would false-trigger the budget-exceeded path)
	sum, _, err := md5FileBudgetedContext(ctx, absPath, item.Size+1)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		s.lg.Warnf("backlog: MD5 failed for %s: %v", item.Rel, err)
		return // keep in backlog; retry next tick
	}

	// Re-verify after hashing: the file may have been modified while we were reading it.
	fi2, err := os.Stat(absPath)
	if err != nil || fi2.Size() != item.Size || fi2.ModTime().Unix() != item.Mtime {
		// File changed during hash — drop stale entry; next scan will re-enqueue with new state.
		s.removeBacklogEntry(item)
		return
	}

	// Deposit MD5 into pendingMD5; scanner applies it at next swap (no direct s.cur write).
	// Match on Rel+Mtime+Size so we don't remove a newer entry if the file changed during hash.
	s.mu.Lock()
	s.pendingMD5[item.Rel] = pendingMD5Entry{MD5: sum, Mtime: item.Mtime, Size: item.Size}
	for i, b := range s.backlog {
		if b.Rel == item.Rel && b.Mtime == item.Mtime && b.Size == item.Size {
			s.backlog = append(s.backlog[:i], s.backlog[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	_ = s.saveBacklog()
	s.lg.Debugf("backlog: MD5 done %s (%.1f MiB)", item.Rel, float64(item.Size)/(1<<20))
}

// removeBacklogEntry drops the entry matching item.Rel+Mtime+Size from s.backlog.
// Matching on all three fields ensures a newer entry (updated by the scanner) is never deleted.
func (s *Scanner) removeBacklogEntry(item backlogItem) {
	s.mu.Lock()
	for i, b := range s.backlog {
		if b.Rel == item.Rel && b.Mtime == item.Mtime && b.Size == item.Size {
			s.backlog = append(s.backlog[:i], s.backlog[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	_ = s.saveBacklog()
}

// md5 with byte budget; returns sum and bytes read
func md5FileBudgeted(path string, budget int64) (string, int64, error) {
	return md5FileBudgetedContext(context.Background(), path, budget)
}

func md5FileBudgetedContext(ctx context.Context, path string, budget int64) (string, int64, error) {
	f, err := os.Open(normalizeForOpen(path))
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := md5.New()
	var copied int64
	buf := make([]byte, 128*1024)
	for budget > 0 {
		if err := ctx.Err(); err != nil {
			return "", copied, err
		}
		n := int64(len(buf))
		if n > budget {
			n = budget
		}
		m, er := io.CopyN(h, f, n)
		copied += m
		budget -= m
		if er == io.EOF {
			break
		}
		if er != nil {
			return "", copied, er
		}
	}
	return hex.EncodeToString(h.Sum(nil)), copied, nil
}

// --- utilities ---

func (s *Scanner) toRelPOSIX(abs string) string {
	abs = pathAbs(abs)
	rel, err := filepath.Rel(s.wc, abs)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(rel, "..") {
		return ""
	}
	return toPOSIX(rel)
}

func toPOSIX(p string) string   { return filepath.ToSlash(p) }
func fromPOSIX(p string) string { return filepath.FromSlash(p) }

func infoType(d fs.DirEntry) fs.FileMode { return d.Type() & os.ModeType }

func pickType(isDir bool) EntryType {
	if isDir {
		return EntryDir
	}
	return EntryFile
}

func isDebug() bool {
	return strings.Contains(strings.ToLower(os.Getenv("FILEES_LOG")), "debug") || strings.Contains(strings.ToLower(os.Getenv("FILEES_LOG")), "trace")
}

func (s *Scanner) isBusy() bool {
	fi, err := os.Stat(s.busyPath)
	if err != nil {
		return false
	}
	// stale?
	if time.Since(fi.ModTime()) > s.busyTTL {
		// Remove it, do not merely ignore it. A marker left behind by a
		// killed commit outlives the process that wrote it, and every later
		// scan re-detects the same file: one working copy produced this
		// warning every ~15s for nine days. Removing it makes the warning
		// fire once, which is what it is worth.
		if err := os.Remove(s.busyPath); err != nil && !os.IsNotExist(err) {
			s.lg.Warnf("commit.busy stale — ignoring (removal failed: %v)", err)
			return false
		}
		s.lg.Warnf("commit.busy stale — removed")
		return false
	}
	return true
}

func pathAbs(p string) string {
	if !filepath.IsAbs(p) {
		ap, _ := filepath.Abs(p)
		p = ap
	}
	return p
}

func normalizeForOpen(p string) string {
	ap := pathAbs(p)
	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(ap, `\\?\`) && len(ap) >= 260 {
			ap = `\\?\` + ap
		}
	}
	return ap
}

func toDiskList(m index) []diskEntry {
	list := make([]diskEntry, 0, len(m))
	for rel, mm := range m {
		de := diskEntry{Path: rel, Mtime: mm.MtimeSec}
		if !mm.IsDir {
			de.Size = mm.Size
			if mm.MD5 != "" {
				de.MD5 = mm.MD5
			}
		}
		list = append(list, de)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	return list
}

// atomicWriteJSON writes v to path through a randomly named temporary file in
// the same directory. The temp name must not be derived from path: these state
// files live inside a synced working copy, so a collaborator can commit an
// svn:special symlink at a predictable "<path>.tmp" and have an ordinary svn
// update materialize it before we write. os.Create follows symlinks and would
// overwrite the link's target; os.CreateTemp creates exclusively, under a name
// the attacker cannot predict. Same idiom as pkg/passport's saveLocked. Rename
// replaces a symlink at the destination rather than following it, so the
// destination itself needs no separate guard.
func atomicWriteJSON(path string, v any) error {
	d := filepath.Dir(path)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	return atomicWriteJSONInExistingDir(path, v)
}

func atomicWriteJSONInExistingDir(path string, v any) error {
	d := filepath.Dir(path)
	f, err := os.CreateTemp(d, ".filees-state-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	// os.CreateTemp opens 0600; keep the mode this state file always had so the
	// only behavioural change here is the symlink fix.
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
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
	return os.Rename(tmp, path)
}

func coalesce(s string, def string) string {
	if s != "" {
		return s
	}
	return def
}

func (s *Scanner) exists(p string) bool { _, err := os.Stat(p); return err == nil }
