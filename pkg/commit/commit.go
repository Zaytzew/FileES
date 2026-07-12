package commit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

// Rules configure commit behaviour.
type Rules struct {
	Window         time.Duration   // commit window (scanPeriod = window/2)
	MaxBatchFiles  int             // max files/dirs per commit
	ShoutPatterns  *regexp.Regexp  // optional; if path matches => create ticket (rate limited)
	LockFirst      bool            // if true, try svn lock before commit
	RateLimitShout time.Duration   // min duration between shouts
	NewLatency     time.Duration   // delay for new (Added) entries before commit (default 5m)
}

// Service wires events → staging → svn → tickets, respecting runtime gates.
type Service struct {
	Cli      client.Client
	Tickets  interface{ CreateNotice(wc string, clientUUID, title, body string) (string, error) }
	Rules    Rules
	HostGate runtime.Gate
	RepoMtx  runtime.RepoMutex
	Logger   talk.Logger
	RepoURL  string

	// internal
	mu       sync.Mutex
	staging  map[string]*stageItem // rel path -> info
	lastShout time.Time
}

type stageItem struct {
	Rel       string
	Abs       string
	OldRel    string         // source path for Renamed; empty otherwise
	IsDir     bool
	Op        watcher.OpType
	FirstSeen time.Time // for Added latency
	ver       uint64   // incremented on every in-place update
}

// Run consumes watcher events and periodically performs commits.
func (s *Service) Run(ctx context.Context, repoID, wc, username, password string, events <-chan watcher.Event) {
	lg := s.Logger
	if s.Rules.NewLatency <= 0 { s.Rules.NewLatency = 5 * time.Minute }
	if s.Rules.MaxBatchFiles <= 0 { s.Rules.MaxBatchFiles = 1000 }
	st := make(map[string]*stageItem)
	s.staging = st

	window := s.Rules.Window
	if window <= 0 { window = 30 * time.Second }
	ticker := time.NewTicker(window)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			lg.Infof("commit service stop")
			return
		case ev, ok := <-events:
			if !ok { return }
			s.addEvent(ev)
		case <-ticker.C:
			if err := s.tryCommit(ctx, wc, username, password); err != nil {
				lg.Warnf("commit attempt failed: %v", err)
			}
		}
	}
}

func (s *Service) addEvent(ev watcher.Event) {
	s.mu.Lock(); defer s.mu.Unlock()
	// normalize key = rel posix path
	key := ev.Rel
	it, ok := s.staging[key]
	if !ok {
		it = &stageItem{Rel: ev.Rel, Abs: ev.Path, OldRel: ev.OldRel, IsDir: ev.Type == watcher.EntryDir, Op: ev.Op, FirstSeen: time.Now()}
		s.staging[key] = it
		return
	}
	// merge ops: Added+Modified -> Added, Modified+Added -> Added, Delete overrides, Renamed wins
	switch ev.Op {
	case watcher.Deleted:
		it.Op = watcher.Deleted
		it.OldRel = ""
	case watcher.Added:
		it.Op = watcher.Added
		it.OldRel = ""
	case watcher.Modified:
		if it.Op != watcher.Added && it.Op != watcher.Deleted { it.Op = watcher.Modified }
	case watcher.Renamed:
		it.Op = watcher.Renamed
		it.OldRel = ev.OldRel
	}
	// refresh Abs/IsDir if needed
	it.Abs = ev.Path
	it.IsDir = (ev.Type == watcher.EntryDir)
	it.ver++
}

func (s *Service) tryCommit(ctx context.Context, wc, username, password string) error {
	s.mu.Lock()
	// snapshot and filter by latency & max batch
	now := time.Now()
	pending := make([]*stageItem, 0, len(s.staging))
	pendingVer := make([]uint64, 0, len(s.staging))
	for _, it := range s.staging {
		if it.Op == watcher.Added {
			if now.Sub(it.FirstSeen) < s.Rules.NewLatency {
				continue
			}
		}
		pending = append(pending, it)
		pendingVer = append(pendingVer, it.ver)
	}
	s.mu.Unlock()

	if len(pending) == 0 { return nil }

	// sort for stable order; cut to MaxBatchFiles
	sort.Slice(pending, func(i, j int) bool { return pending[i].Rel < pending[j].Rel })
	if len(pending) > s.Rules.MaxBatchFiles { pending = pending[:s.Rules.MaxBatchFiles] }

	// wstępne listy
	var addPaths, delPaths, commitPaths []string
	var renamedItems []*stageItem
	for _, it := range pending {
		switch it.Op {
		case watcher.Added:
			addPaths = append(addPaths, it.Rel)
			commitPaths = append(commitPaths, it.Rel)
		case watcher.Modified:
			commitPaths = append(commitPaths, it.Rel)
		case watcher.Deleted:
			delPaths = append(delPaths, it.Rel)
		case watcher.Renamed:
			renamedItems = append(renamedItems, it)
			commitPaths = append(commitPaths, it.OldRel, it.Rel)
		}
	}

	// --- FILTR STAGINGU PRZEZ `svn status` ---
	all := dedup(append(append([]string{}, addPaths...), delPaths...))
	st := s.statusMap(ctx, wc, username, password, all)

	// ADD: tylko unversioned
	filteredAdd := make([]string, 0, len(addPaths))
	for _, p := range addPaths {
		item := st[p]
		if item == "unversioned" || item == "" {
			filteredAdd = append(filteredAdd, p)
		} else {
			s.Logger.Debugf("skip add %s (status=%s)", p, item)
		}
	}
	addPaths = filteredAdd

	// DELETE: rozróżnij systemowe usunięcia (missing) od już zestejdżowanych (deleted)
	var toSvnDelete, alreadyStaged []string
	for _, p := range delPaths {
		switch st[p] {
		case "missing", "normal", "modified":
			toSvnDelete = append(toSvnDelete, p)
		case "deleted":
			alreadyStaged = append(alreadyStaged, p)
			s.Logger.Debugf("skip svn delete %s (already staged)", p)
		default:
			s.Logger.Debugf("skip delete %s (status=%s)", p, st[p])
		}
	}
	delPaths = append(toSvnDelete, alreadyStaged...)
	commitPaths = append(commitPaths, delPaths...)
	// --- KONIEC FILTRA ---

	// rate-limited shout
	if s.Rules.ShoutPatterns != nil && time.Since(s.lastShout) >= s.Rules.RateLimitShout {
		for _, p := range commitPaths {
			if s.Rules.ShoutPatterns.MatchString(p) {
				_ = s.makeNotice(wc, fmt.Sprintf("Pending commit: %d paths", len(commitPaths)), strings.Join(commitPaths, "\n"))
				s.lastShout = time.Now()
				break
			}
		}
	}

	// gates
	if s.HostGate != nil {
		release, err := s.HostGate.Acquire(ctx)
		if err != nil { return err }
		defer release()
	}
	if s.RepoMtx != nil {
		unlock, err := s.RepoMtx.Lock(ctx, s.RepoURL)
		if err != nil { return err }
		defer unlock()
	}

	// busy flag
	busy := filepath.Join(wc, ".filees", "state", "commit.busy")
	if err := atomicWriteString(busy, fmt.Sprintf("ts_start=%d\npid=%d\nrepo=%s\n", time.Now().Unix(), os.Getpid(), s.RepoURL)); err == nil {
		defer os.Remove(busy)
	}

	// staging
	if len(addPaths) > 0 {
		if out, err := s.Cli.Add(ctx, wc, addPaths, username, password); err != nil {
			s.Logger.Warnf("svn add failed: %v\n%s", err, out)
		}
	}
	if len(toSvnDelete) > 0 {
		if out, err := s.Cli.Delete(ctx, wc, toSvnDelete, username, password); err != nil {
			s.Logger.Warnf("svn delete failed: %v\n%s", err, out)
		}
	}
	for _, it := range renamedItems {
		if out, err := s.Cli.Delete(ctx, wc, []string{it.OldRel}, username, password); err != nil {
			s.Logger.Warnf("svn delete (rename src) %s: %v\n%s", it.OldRel, err, out)
		}
		if out, err := s.Cli.Add(ctx, wc, []string{it.Rel}, username, password); err != nil {
			s.Logger.Warnf("svn add (rename dst) %s: %v\n%s", it.Rel, err, out)
		}
	}
	if s.Rules.LockFirst && len(commitPaths) > 0 {
		if out, err := s.Cli.Lock(ctx, wc, commitPaths, username, password); err != nil {
			s.Logger.Warnf("svn lock failed: %v\n%s", err, out)
		}
	}

	// commit
	msg := fmt.Sprintf("FileES auto-commit: %d paths", len(commitPaths))
	out, err := s.Cli.Commit(ctx, wc, commitPaths, msg, username, password)
	if err != nil { return fmt.Errorf("svn commit: %w\n%s", err, out) }

	// head.rev
	if rev := parseRevision(out); rev != "" {
		head := filepath.Join(wc, ".filees", "state", "head.rev")
		_ = atomicWriteString(head, rev+"\n")
	}

	// cleanup staging — only remove items unchanged since snapshot
	s.mu.Lock()
	for i, it := range pending {
		if cur, ok := s.staging[it.Rel]; ok && cur == it && cur.ver == pendingVer[i] {
			delete(s.staging, it.Rel)
		}
	}
	s.mu.Unlock()
	return nil
}


func (s *Service) makeNotice(wc, title, body string) error {
	if s.Tickets == nil { return nil }
	_, err := s.Tickets.CreateNotice(wc, clientUUID(), title, body)
	return err
}

// --- helpers ---

func parseRevision(out string) string {
	// common patterns:
	// "Committed revision 123."
	// "At revision 456."
	fields := strings.Fields(out)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "revision" {
			return strings.TrimRight(fields[i+1], ".")
		}
	}
	return ""
}

func clientUUID() string { return "client-unknown" }

func atomicWriteString(path string, data string) error {
	d := filepath.Dir(path)
	if err := os.MkdirAll(d, 0o755); err != nil { return err }
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil { return err }
	return os.Rename(tmp, path)
}

// dedup usuwa duplikaty ścieżek (REL, POSIX).
func dedup(in []string) []string {
	m := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if _, ok := m[p]; ok {
			continue
		}
		m[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// statusMap pobiera mapę rel-path -> svn status item ("unversioned","normal","modified","missing",...).
func (s *Service) statusMap(ctx context.Context, wc, username, password string, paths []string) map[string]string {
	out := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return out
	}
	st, err := s.Cli.Status(ctx, wc, paths, username, password)
	if err != nil {
		s.Logger.Warnf("svn status failed: %v", err)
		return out
	}
	for _, e := range st {
		// Upewnij się, że mamy POSIX (watcher emituje REL w POSIX)
		p := strings.ReplaceAll(e.Path, "\\", "/")
		out[p] = e.Item
	}
	return out
}

