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
)

// Event sent to commit.Service
// Path = ABS path (for stat/IO), Rel = POSIX path relative to WC (for SVN)
type Event struct {
	Path string    // absolute path
	Rel  string    // posix relative path
	Type EntryType // file | dir
	Op   OpType    // Added | Modified | Deleted
}

// Options control scanner behaviour
// Times/intervals default to sane values if zero.
type Options struct {
	WC              string        // ABS root of working copy (must exist)
	StatePath       string        // .filees/state/manifest.json
	ScanPeriod      time.Duration // typical: commit window / 2 (default 15s)
	BusyPath        string        // .filees/state/commit.busy
	BusyTTL         time.Duration // default 10m (stale busy ignore)
	TicketsPoll     time.Duration // poll tickets-only while busy (default 12s)
	DeletedDebounce time.Duration // emit FileDeleted after this absence (default 10m)
	IgnoreRegex     *regexp.Regexp // optional global regex (extra filter)
	LogScope        string

	// MD5 / hashing heuristics
	UseMD5           bool          // default true (tri-state semantics)
	MD5PerFileCutoff int64         // bytes; default 64*MiB
	MD5BudgetBytes   int64         // per scan; default 256*MiB
	MD5BudgetFrac    float64       // fraction of ScanPeriod time; default 0.30

	// Event channel buffer size
	ChanSize int // default 1024
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

	useMD5           bool
	md5Cutoff        int64
	md5BudgetBytes   int64
	md5BudgetFrac    float64
	chanSize         int

	// dynamic
	mu           sync.Mutex
	cur          index              // current manifest snapshot (ACTIVE) or building (BASELINING)
	missingSince map[string]time.Time

	// ignores (hot-reload user cfg)
	ignorePath   string    // .filees/user_ignore.cfg
	ignoreMTime  time.Time
	hardGlobs []glob
	softGlobs []glob
	igRegex   *regexp.Regexp

	// md5 backlog persistence
	backlogPath string // .filees/state/md5.backlog.json
	backlog     []backlogItem

	// mode
	mode mode
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

func NewScanner(opts Options) (Scanner, error) {
	if opts.WC == "" { return Scanner{}, errors.New("watcher: Options.WC is empty") }
	fi, err := os.Stat(opts.WC)
	if err != nil || !fi.IsDir() { return Scanner{}, errors.New("watcher: WC missing or not a directory") }

	if opts.ScanPeriod <= 0 { opts.ScanPeriod = 15 * time.Second }
	if opts.BusyTTL <= 0 { opts.BusyTTL = 10 * time.Minute }
	if opts.TicketsPoll <= 0 { opts.TicketsPoll = 12 * time.Second }
	if opts.DeletedDebounce <= 0 { opts.DeletedDebounce = 10 * time.Minute }
	if opts.ChanSize <= 0 { opts.ChanSize = 1024 }
	if opts.MD5PerFileCutoff <= 0 { opts.MD5PerFileCutoff = 64 * 1024 * 1024 }
	if opts.MD5BudgetBytes <= 0 { opts.MD5BudgetBytes = 256 * 1024 * 1024 }
	if opts.MD5BudgetFrac <= 0 { opts.MD5BudgetFrac = 0.30 }
	if opts.UseMD5 == false { /* explicit */ } else { opts.UseMD5 = true }

	wc := filepath.Clean(opts.WC)
	stateDir := filepath.Join(wc, ".filees", "state")
	ignorePath := filepath.Join(wc, ".filees", "user_ignore.cfg")
	backlogPath := filepath.Join(stateDir, "md5.backlog.json")

	s := Scanner{
		wc:            wc,
		statePath:     opts.StatePath,
		period:        opts.ScanPeriod,
		busyPath:      coalesce(opts.BusyPath, filepath.Join(stateDir, "commit.busy")),
		busyTTL:       opts.BusyTTL,
		ticketsInt:    opts.TicketsPoll,
		debounceD:     opts.DeletedDebounce,
		lg:            talk.With(opts.LogScope),
		useMD5:        opts.UseMD5,
		md5Cutoff:     opts.MD5PerFileCutoff,
		md5BudgetBytes: opts.MD5BudgetBytes,
		md5BudgetFrac:  opts.MD5BudgetFrac,
		chanSize:      opts.ChanSize,
		cur:           make(index),
		missingSince:  make(map[string]time.Time),
		ignorePath:    ignorePath,
		backlogPath:   backlogPath,
		igRegex:       opts.IgnoreRegex,
		mode:          modeBaselining,
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
	// load ignores and backlog (best-effort)
	_ = s.reloadIgnores()
	_ = s.loadBacklog()
	return s, nil
}

// Start scanning; returns read-only event channel
func (s *Scanner) Start(ctx context.Context) <-chan Event {
	events := make(chan Event, s.chanSize)
	go s.loop(ctx, events)
	return events
}

// LoadState reads manifest.json (list) and builds in-memory map
func (s *Scanner) LoadState(path string) error {
	s.mu.Lock(); defer s.mu.Unlock()
	f, err := os.Open(path)
	if err != nil { return err }
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var list []diskEntry
	if err := dec.Decode(&list); err != nil { return err }
	m := make(index, len(list))
	for _, e := range list {
		isDir := strings.HasSuffix(e.Path, "/")
		m[e.Path] = meta{MtimeSec: e.Mtime, Size: e.Size, MD5: e.MD5, IsDir: isDir}
	}
	s.cur = m
	return nil
}

// SaveState writes the current map as a sorted list to path (atomically)
func (s *Scanner) SaveState(path string) error {
	s.mu.Lock(); defer s.mu.Unlock()
	list := make([]diskEntry, 0, len(s.cur))
	for rel, m := range s.cur {
		de := diskEntry{Path: rel, Mtime: m.MtimeSec}
		if !m.IsDir { de.Size = m.Size; if m.MD5 != "" { de.MD5 = m.MD5 } }
		list = append(list, de)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	if err := atomicWriteJSON(path, list); err != nil { return err }
	return nil
}

// --- main loop ---

func (s *Scanner) loop(ctx context.Context, out chan<- Event) {
	defer close(out)
	ticker := time.NewTicker(s.period)
	defer ticker.Stop()

	// initial pass
	s.scanCycle(ctx, out)

	for {
		select {
		case <-ctx.Done():
			// best-effort save
			if s.statePath != "" { _ = s.SaveState(s.statePath) }
			return
		case <-ticker.C:
			s.scanCycle(ctx, out)
		}
	}
}

func (s *Scanner) scanCycle(ctx context.Context, out chan<- Event) {
	start := time.Now()
	var aCnt, mCnt, dCnt, igCnt, md5Done, md5Skipped int
	busy := s.isBusy()

	if s.mode == modeBaselining {
		// Build manifest.tmp silently
		_ = s.reloadIgnores()
		stateDir := filepath.Dir(s.statePath)
		tmpPath := filepath.Join(stateDir, "manifest.tmp")
		mp := s.scanTree(ctx, &aCnt, &mCnt, &dCnt, &igCnt, &md5Done, &md5Skipped, /*emit=*/false, out, busy)
		// write tmp
		_ = atomicWriteJSON(tmpPath, toDiskList(mp))
		// promote?
		if s.exists(filepath.Join(s.wc, ".filees", "state", "baseline.ok")) {
			// require manifest.tmp
			if s.exists(tmpPath) {
				// mv tmp -> manifest.json
				_ = os.MkdirAll(filepath.Dir(s.statePath), 0o755)
				_ = os.Rename(tmpPath, s.statePath)
				_ = os.Remove(filepath.Join(s.wc, ".filees", "state", "baseline.ok"))
				// load into RAM and switch mode
				_ = s.LoadState(s.statePath)
				s.mode = modeActive
				s.lg.Infof("PROMOTE baseline → active")
			}
		} baseline → active")
			}
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
		_ = s.loadBacklog()
		mp := s.scanTree(ctx, &aCnt, &mCnt, &dCnt, &igCnt, &md5Done, &md5Skipped, /*emit=*/true, out, false)
		// swap in-memory state
		s.mu.Lock(); s.cur = mp; s.mu.Unlock()
		// persist backlog best-effort
		_ = s.saveBacklog()
	}

	if (aCnt+mCnt+dCnt) > 0 {
		dur := time.Since(start)
		s.lg.Infof("scan done in %s (A=%d M=%d D=%d) backlog=%d md5_done=%d md5_skipped=%d ignored=%d busy=%t",
			dur, aCnt, mCnt, dCnt, len(s.backlog), md5Done, md5Skipped, igCnt, busy)
	} else if isDebug() {
		dur := time.Since(start)
		s.lg.Debugf("scan done in %s (A=%d M=%d D=%d) backlog=%d md5_done=%d md5_skipped=%d ignored=%d busy=%t",
			dur, aCnt, mCnt, dCnt, len(s.backlog), md5Done, md5Skipped, igCnt, busy)
	}
}

// scanTree walks the WC and returns a fresh index. If emit==true, it emits events based on diff vs current state.
func (s *Scanner) scanTree(ctx context.Context, aCnt, mCnt, dCnt, igCnt, md5Done, md5Skipped *int, emit bool, out chan<- Event, ticketsOnly bool) index {
	curr := make(index)
	deleted := make(map[string]meta)

	s.mu.Lock()
	for rel, m := range s.cur { deleted[rel] = m }
	s.mu.Unlock()

	md5BytesBudget := s.md5BudgetBytes
	timeBudget := time.Duration(float64(s.period) * s.md5BudgetFrac)
	budgetDeadline := time.Now().Add(timeBudget)

	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil { s.lg.Warnf("walk error: %s: %v", path, err); return nil }
		// ignore any symlink (file or dir) early
		if d.Type()&os.ModeSymlink != 0 { *igCnt++; return nil }

		name := d.Name()
		if d.IsDir() {
			if name == ".svn" { return fs.SkipDir }
		}

		rel := s.toRelPOSIX(path)
		if rel == "" { return nil }

		// .filees selective filters
		if strings.HasPrefix(rel, ".filees/") {
			if strings.HasPrefix(rel, ".filees/state/") || strings.HasPrefix(rel, ".filees/locks/") {
				if d.IsDir() { return fs.SkipDir }
				*igCnt++; return nil
			}
			if ticketsOnly && !strings.HasPrefix(rel, ".filees/tickets/") {
				if d.IsDir() { return fs.SkipDir }
				*igCnt++; return nil
			}
		}

		// user ignores (hard first)
		if s.isIgnored(rel, d.IsDir()) { *igCnt++; if d.IsDir() { return fs.SkipDir }; return nil }

		// gather info
		info, ierr := d.Info(); if ierr != nil { s.lg.Warnf("stat error: %s: %v", path, ierr); return nil }
		isDir := info.IsDir()

		// record into curr snapshot
		m := meta{ MtimeSec: info.ModTime().Unix(), IsDir: isDir }
		if !isDir { m.Size = info.Size() }
		curr[rel] = m

		old, had := s.cur[rel]

		// decide events
		if emit {
			if !had {
				out <- Event{Path: pathAbs(path), Rel: rel, Type: pickType(isDir), Op: Added}
				*aCnt++
			} else {
				changed := false
				if isDir {
					changed = false // mtime-only dir changes ignored
				} else {
					if m.Size != old.Size || m.MtimeSec != old.MtimeSec {
						if s.useMD5 {
							if m.Size <= s.md5Cutoff && md5BytesBudget > 0 && time.Now().Before(budgetDeadline) {
								sum, readB, herr := md5FileBudgeted(path, md5BytesBudget)
								if herr == nil { m.MD5 = sum; md5BytesBudget -= readB; *md5Done++ } else { *md5Skipped++ }
							} else {
								s.enqueueBacklog(rel, m.Size, m.MtimeSec); *md5Skipped++
							}
						}
						changed = true
					}
				}
				if changed { out <- Event{Path: pathAbs(path), Rel: rel, Type: EntryFile, Op: Modified}; *mCnt++ }
				delete(deleted, rel)
			}
		} else {
			// baselining: pre-fill MD5 under budget
			if !isDir && s.useMD5 && m.Size <= s.md5Cutoff && md5BytesBudget > 0 && time.Now().Before(budgetDeadline) {
				sum, readB, herr := md5FileBudgeted(path, md5BytesBudget)
				if herr == nil { m.MD5 = sum; md5BytesBudget -= readB; *md5Done++ } else { *md5Skipped++ }
				curr[rel] = m
			} else if !isDir && s.useMD5 && m.Size > s.md5Cutoff { s.enqueueBacklog(rel, m.Size, m.MtimeSec) }
		}
		return nil
	}

	_ = filepath.WalkDir(s.wc, walkFn)

	// handle deletions (emit only if debounce exceeded; in baselining we emit nothing)
	if emit {
		now := time.Now()
		for rel, old := range deleted {
			// if directory missing -> rmdir (Deleted) immediately (no debounce?) — stosujemy T=10m jak dla plików, dla spójności
			first, seen := s.missingSince[rel]
			if !seen { s.missingSince[rel] = now; continue }
			if now.Sub(first) >= s.debounceD {
				out <- Event{Path: filepath.Join(s.wc, fromPOSIX(rel)), Rel: rel, Type: pickType(old.IsDir), Op: Deleted}
				*dCnt++
				delete(s.missingSince, rel)
			}
		}
	} else {
		// baselining resets debounce map
		s.missingSince = make(map[string]time.Time)
	}

	return curr
}

// tickets-only quick scan while busy; we emit events for changes under .filees/tickets/
func (s *Scanner) scanTicketsOnly(out chan<- Event) {
	root := filepath.Join(s.wc, ".filees", "tickets")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil { return nil }
		if d.IsDir() { return nil }
		rel := s.toRelPOSIX(path)
		if rel == "" { return nil }
		out <- Event{Path: pathAbs(path), Rel: rel, Type: EntryFile, Op: Modified}
		return nil
	})
}

// --- ignores ---

type glob struct{ raw string }

func (g glob) match(rel string, isDir bool) bool {
	p := g.raw
	// hard ignore indicated by leading '!'; we strip it here; semantics handled by caller
	if len(p) > 0 && p[0] == '!' { p = p[1:] }
	// very small glob implementation: '**' match any, '*' match segment, suffix/prefix
	// For simplicity, use filepath.Match with POSIX slashes by converting back.
	// We ensure patterns themselves are POSIX (loaded as-is from cfg).
	ok, _ := filepath.Match(fromPOSIX(p), fromPOSIX(rel))
	if ok { return true }
	// handle '**' by naive contains check when pattern has "**/"
	if strings.Contains(p, "**/") {
		pp := strings.ReplaceAll(p, "**/", "")
		return strings.Contains(rel, pp)
	}
	return false
}

func (s *Scanner) isIgnored(rel string, isDir bool) bool {
	// global regex first (from config), if provided
	if s.igRegex != nil && s.igRegex.MatchString(rel) { return true }
	for _, g := range s.hardGlobs { if g.match(rel, isDir) { return true } }
	for _, g := range s.softGlobs { if g.match(rel, isDir) { return true } }
	return false
} }
	for _, g := range s.softGlobs { if g.match(rel, isDir) { return true } }
	return false
}

func (s *Scanner) reloadIgnores() error {
	fi, err := os.Stat(s.ignorePath)
	if err != nil { return nil }
	if !fi.ModTime().After(s.ignoreMTime) { return nil }
	f, err := os.Open(s.ignorePath)
	if err != nil { return err }
	defer f.Close()
	s.hardGlobs = nil; s.softGlobs = nil
	s.ignoreMTime = fi.ModTime()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") { continue }
		// normalize to POSIX
		line = toPOSIX(line)
		if strings.HasPrefix(line, "!") { s.hardGlobs = append(s.hardGlobs, glob{raw: line}); continue }
		s.softGlobs = append(s.softGlobs, glob{raw: line})
	}
	return nil
}

// --- md5 helpers / backlog ---

func (s *Scanner) enqueueBacklog(rel string, size int64, mtime int64) {
	bi := backlogItem{Rel: rel, Size: size, Mtime: mtime, QueuedAt: time.Now().Unix()}
	s.backlog = append(s.backlog, bi)
	// limit backlog size
	if len(s.backlog) > 1000 { s.backlog = s.backlog[len(s.backlog)-1000:] }
}

func (s *Scanner) loadBacklog() error {
	f, err := os.Open(s.backlogPath)
	if err != nil { return nil }
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var list []backlogItem
	if err := dec.Decode(&list); err != nil { return nil }
	// prioritize: newer mtime first; then bigger files
	sort.Slice(list, func(i, j int) bool {
		if list[i].Mtime == list[j].Mtime { return list[i].Size > list[j].Size }
		return list[i].Mtime > list[j].Mtime
	})
	s.backlog = list
	return nil
}

func (s *Scanner) saveBacklog() error {
	return atomicWriteJSON(s.backlogPath, s.backlog)
}

// md5 with byte budget; returns sum and bytes read
func md5FileBudgeted(path string, budget int64) (string, int64, error) {
	f, err := os.Open(normalizeForOpen(path))
	if err != nil { return "", 0, err }
	defer f.Close()
	h := md5.New()
	var copied int64
	buf := make([]byte, 128*1024)
	for budget > 0 {
		n := int64(len(buf))
		if n > budget { n = budget }
		m, er := io.CopyN(h, f, n)
		copied += m
		budget -= m
		if er == io.EOF { break }
		if er != nil { return "", copied, er }
	}
	return hex.EncodeToString(h.Sum(nil)), copied, nil
}

// --- utilities ---

func (s *Scanner) toRelPOSIX(abs string) string {
	abs = pathAbs(abs)
	rel, err := filepath.Rel(s.wc, abs)
	if err != nil { return "" }
	if strings.HasPrefix(rel, "..") { return "" }
	return toPOSIX(rel)
}

func toPOSIX(p string) string { return filepath.ToSlash(p) }
func fromPOSIX(p string) string { return filepath.FromSlash(p) }

func infoType(d fs.DirEntry) fs.FileMode { return d.Type() & os.ModeType }

func pickType(isDir bool) EntryType { if isDir { return EntryDir }; return EntryFile }

func isDebug() bool { return strings.Contains(strings.ToLower(os.Getenv("FILEES_LOG")), "debug") || strings.Contains(strings.ToLower(os.Getenv("FILEES_LOG")), "trace") }

func (s *Scanner) isBusy() bool {
	fi, err := os.Stat(s.busyPath)
	if err != nil { return false }
	// stale?
	if time.Since(fi.ModTime()) > s.busyTTL { s.lg.Warnf("commit.busy stale — ignoring"); return false }
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
		if !strings.HasPrefix(ap, `\?\`) && len(ap) >= 260 {
			ap = `\?\` + ap
		}
	}
	return ap
}
	}
	return ap
}

func toDiskList(m index) []diskEntry {
	list := make([]diskEntry, 0, len(m))
	for rel, mm := range m {
		de := diskEntry{Path: rel, Mtime: mm.MtimeSec}
		if !mm.IsDir { de.Size = mm.Size; if mm.MD5 != "" { de.MD5 = mm.MD5 } }
		list = append(list, de)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	return list
}

func atomicWriteJSON(path string, v any) error {
	d := filepath.Dir(path)
	if err := os.MkdirAll(d, 0o755); err != nil { return err }
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil { return err }
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil { f.Close(); _ = os.Remove(tmp); return err }
	_ = f.Sync(); _ = f.Close()
	return os.Rename(tmp, path)
}

func coalesce(s string, def string) string { if s != "" { return s }; return def }

func (s *Scanner) exists(p string) bool { _, err := os.Stat(p); return err == nil }
