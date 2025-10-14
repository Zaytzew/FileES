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
	IsDir     bool
	Op        watcher.OpType
	FirstSeen time.Time // for Added latency
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
		it = &stageItem{Rel: ev.Rel, Abs: ev.Path, IsDir: ev.Type == watcher.EntryDir, Op: ev.Op, FirstSeen: time.Now()}
		s.staging[key] = it
		return
	}
	// merge ops: Added+Modified -> Added, Modified+Added -> Added, Delete overrides
	switch ev.Op {
	case watcher.Deleted:
		it.Op = watcher.Deleted
	case watcher.Added:
		if it.Op != watcher.Deleted { it.Op = watcher.Added }
	case watcher.Modified:
		if it.Op != watcher.Added && it.Op != watcher.Deleted { it.Op = watcher.Modified }
	}
	// refresh Abs/IsDir if needed
	it.Abs = ev.Path
	it.IsDir = (ev.Type == watcher.EntryDir)
}

func (s *Service) tryCommit(ctx context.Context, wc, username, password string) error {
	s.mu.Lock()
	// snapshot and filter by latency & max batch
	now := time.Now()
	pending := make([]*stageItem, 0, len(s.staging))
	for _, it := range s.staging {
		if it.Op == watcher.Added {
			if now.Sub(it.FirstSeen) < s.Rules.NewLatency { continue }
		}
		pending = append(pending, it)
	}
	s.mu.Unlock()

	if len(pending) == 0 { return nil }

	// sort for stable order: dirs first for deletions, then files; by path
	sort.Slice(pending, func(i, j int) bool { return pending[i].Rel < pending[j].Rel })
	if len(pending) > s.Rules.MaxBatchFiles { pending = pending[:s.Rules.MaxBatchFiles] }

	// prepare lists
	var addPaths, delPaths, commitPaths []string
	for _, it := range pending {
		switch it.Op {
		case watcher.Added:
			addPaths = append(addPaths, it.Rel)
			commitPaths = append(commitPaths, it.Rel)
		case watcher.Modified:
			commitPaths = append(commitPaths, it.Rel)
		case watcher.Deleted:
			delPaths = append(delPaths, it.Rel)
		}
	}

	// shout ticket (rate-limited) if any path matches
	if s.Rules.ShoutPatterns != nil && time.Since(s.lastShout) >= s.Rules.RateLimitShout {
		for _, p := range commitPaths {
			if s.Rules.ShoutPatterns.MatchString(p) {
				_ = s.makeNotice(wc, fmt.Sprintf("Pending commit: %d paths", len(commitPaths)), strings.Join(commitPaths, "\n"))
				s.lastShout = time.Now()
				break
			}
		}
	}

	// Acquire gates
	if s.HostGate != nil {
		if err := s.HostGate.Acquire(ctx); err != nil { return err }
		defer s.HostGate.Release()
	}
	if s.RepoMtx != nil {
		if err := s.RepoMtx.Lock(ctx, s.RepoURL); err != nil { return err }
		defer s.RepoMtx.Unlock(s.RepoURL)
	}

	busy := filepath.Join(wc, ".filees", "state", "commit.busy")
	if err := atomicWriteString(busy, fmt.Sprintf("ts_start=%d\npid=%d\nrepo=%s\n", time.Now().Unix(), os.Getpid(), s.RepoURL)); err == nil {
		defer os.Remove(busy)
	}

	// staging ops
	if len(addPaths) > 0 {
		if out, err := s.Cli.Add(ctx, wc, addPaths, username, password); err != nil { s.Logger.Warnf("svn add failed: %v\n%s", err, out) }
	}
	if len(delPaths) > 0 {
		if out, err := s.Cli.Delete(ctx, wc, delPaths, username, password); err != nil { s.Logger.Warnf("svn delete failed: %v\n%s", err, out) }
	}
	// optional lock-first
	if s.Rules.LockFirst && len(commitPaths) > 0 {
		if out, err := s.Cli.Lock(ctx, wc, commitPaths, username, password); err != nil { s.Logger.Warnf("svn lock failed: %v\n%s", err, out) }
	}

	// commit
	msg := fmt.Sprintf("FileES auto-commit: %d paths", len(commitPaths))
	out, err := s.Cli.Commit(ctx, wc, commitPaths, msg, username, password)
	if err != nil { return fmt.Errorf("svn commit: %w\n%s", err, out) }

	// parse revision and write head.rev
	if rev := parseRevision(out); rev != "" {
		head := filepath.Join(wc, ".filees", "state", "head.rev")
		_ = atomicWriteString(head, rev+"\n")
	}

	// clear committed entries from staging
	s.mu.Lock()
	for _, it := range pending { delete(s.staging, it.Rel) }
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
