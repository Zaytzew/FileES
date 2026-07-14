package commit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errmap"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

// SizeTier mapuje górną granicę rozmiaru batcha na minimalny odstęp między commitami.
// Tiery powinny być posortowane rosnąco według MaxBytes.
// MaxBytes == 0 oznacza catch-all (pasuje do każdego rozmiaru).
type SizeTier struct {
	MaxBytes int64
	Interval time.Duration
}

// Rules configure commit behaviour.
type Rules struct {
	Window            time.Duration  // commit window (scanPeriod = window/2)
	MaxBatchFiles     int            // maximum physical files per commit; directories do not consume a slot
	MaxBatchBytes     int64          // target maximum payload; one oversized file is allowed alone
	BacklogFlushBytes int64          // force a batch when accumulated payload reaches this watermark
	ShutdownTimeout   time.Duration  // graceful final drain deadline
	ShoutPatterns     *regexp.Regexp // optional; if path matches => create ticket (rate limited)
	LockFirst         bool           // if true, try svn lock before commit
	RateLimitShout    time.Duration  // min duration between shouts
	NewLatency        time.Duration  // delay for new (Added) entries before commit (default 5m)
	SizeTiers         []SizeTier     // size-adaptive intervals; empty = use Window only
	PollInterval      time.Duration  // HEAD polling interval; 0 = disabled
}

// effectiveInterval zwraca minimalny wymagany odstęp między commitami dla batcha o rozmiarze totalBytes.
// Zwraca 0 jeśli SizeTiers jest puste (brak ograniczeń).
func (r *Rules) effectiveInterval(totalBytes int64) time.Duration {
	for _, t := range r.SizeTiers {
		if t.MaxBytes <= 0 || totalBytes <= t.MaxBytes {
			return t.Interval
		}
	}
	if len(r.SizeTiers) > 0 {
		return r.SizeTiers[len(r.SizeTiers)-1].Interval
	}
	return 0
}

// Service wires events → staging → svn → tickets, respecting runtime gates.
type Service struct {
	Cli     client.Client
	Tickets interface {
		CreateNotice(wc string, clientUUID, title, body string) (string, error)
	}
	Rules    Rules
	HostGate runtime.Gate
	RepoMtx  runtime.RepoMutex
	Logger   talk.Logger
	RepoURL  string
	UUID     string       // stable client UUID (persisted in .filees/state/client.uuid)
	ErrSink  *errmap.Sink // optional; structured error log (JSON Lines)
	// OnConnectivity is called (async) when online/offline state changes.
	// Argument is "online" or "offline". May be nil.
	OnConnectivity func(string)
	// Emit, if non-nil, is called to publish IPC events (commit.completed, sync.completed, etc.).
	// The closure supplied by main.go wraps ipcserver.Server.Emit with the repo ID.
	Emit func(evType string, payload any)

	// internal
	repoID     string // set from Run(); used by emit()
	mu         sync.Mutex
	staging    map[string]*stageItem // rel path -> info
	cachePath  string                // .filees/commit_cache/cache.json
	lastShout  time.Time
	lastCommit time.Time // last successful commit (for size-adaptive interval)

	// offline state — guarded by offMu (accessed from multiple goroutines)
	offMu     sync.Mutex
	offline   bool
	nextRetry time.Time
	backoff   time.Duration
}

type stageItem struct {
	Rel       string
	Abs       string
	OldRel    string // source path for Renamed; empty otherwise
	IsDir     bool
	Op        watcher.OpType
	FirstSeen time.Time // for Added latency
	ver       uint64    // incremented on every in-place update
}

func (s *Service) goOffline() {
	s.offMu.Lock()
	wasOnline := !s.offline
	s.offline = true
	s.backoff = nextBackoff(s.backoff)
	s.nextRetry = time.Now().Add(s.backoff)
	s.offMu.Unlock()

	if wasOnline {
		s.Logger.Warnf("offline: network unreachable — queuing changes locally")
		s.ErrSink.Emit(errmap.Entry{
			Code:     errmap.CodeNetUnreachable,
			Severity: errmap.SevWarn,
			Hint:     errmap.HintRetryBackoff,
			Msg:      "Network unreachable — queuing changes locally",
		})
		if s.OnConnectivity != nil {
			go s.OnConnectivity("offline")
		}
		s.emit(contract.EvOfflineDetected, nil)
	}
}

func (s *Service) goOnline() {
	s.offMu.Lock()
	wasOffline := s.offline
	s.offline = false
	s.backoff = 0
	s.offMu.Unlock()

	if wasOffline {
		s.Logger.Infof("online: connection restored")
		if s.OnConnectivity != nil {
			go s.OnConnectivity("online")
		}
		s.emit(contract.EvOnlineRestored, nil)
	}
}

// emit publishes an IPC event if the Emit callback is wired.
func (s *Service) emit(evType string, payload any) {
	if s.Emit != nil {
		s.Emit(evType, payload)
	}
}

// isOfflineBackoff returns true when we're offline and still within the backoff window.
func (s *Service) isOfflineBackoff() bool {
	s.offMu.Lock()
	defer s.offMu.Unlock()
	return s.offline && time.Now().Before(s.nextRetry)
}

func nextBackoff(cur time.Duration) time.Duration {
	const max = 5 * time.Minute
	if cur <= 0 {
		return 5 * time.Second
	}
	if next := cur * 2; next < max {
		return next
	}
	return max
}

// Run consumes watcher events and periodically performs commits.
func (s *Service) Run(ctx context.Context, repoID, wc, username, password string, events <-chan watcher.Event) {
	s.repoID = repoID
	lg := s.Logger
	if s.Rules.NewLatency <= 0 {
		s.Rules.NewLatency = 5 * time.Minute
	}
	if s.Rules.MaxBatchFiles <= 0 {
		s.Rules.MaxBatchFiles = 1000
	}
	if s.Rules.MaxBatchBytes <= 0 {
		s.Rules.MaxBatchBytes = 512 * 1024 * 1024
	}
	if s.Rules.BacklogFlushBytes <= 0 {
		s.Rules.BacklogFlushBytes = 1024 * 1024 * 1024
	}
	if s.Rules.ShutdownTimeout <= 0 {
		s.Rules.ShutdownTimeout = 10 * time.Minute
	}
	st := make(map[string]*stageItem)
	s.staging = st

	s.cachePath = filepath.Join(wc, ".filees", "commit_cache", "cache.json")
	if err := os.MkdirAll(filepath.Dir(s.cachePath), 0o755); err != nil {
		lg.Warnf("commit cache dir: %v — cache disabled", err)
		s.cachePath = ""
	} else {
		s.loadCache()
	}

	window := s.Rules.Window
	if window <= 0 {
		window = 30 * time.Second
	}
	ticker := time.NewTicker(window)
	defer ticker.Stop()

	if s.Rules.PollInterval > 0 {
		go s.runPoller(ctx, wc, username, password)
	}

	for {
		select {
		case <-ctx.Done():
			// The watcher closes its channel after its final state save. Consume
			// everything it emitted before beginning the bounded shutdown drain.
			for ev := range events {
				s.addEvent(ev)
			}
			s.shutdownDrain(wc, username, password)
			lg.Infof("commit service stop")
			return
		case ev, ok := <-events:
			if !ok {
				s.shutdownDrain(wc, username, password)
				lg.Infof("commit service stop")
				return
			}
			s.addEvent(ev)
			s.saveCache()
			if s.stagingBytes() >= s.Rules.BacklogFlushBytes {
				if err := s.tryCommitMode(ctx, wc, username, password, true); err != nil {
					lg.Warnf("high-water commit failed: %v", err)
				}
				s.saveCache()
			}
		case <-ticker.C:
			if s.isOfflineBackoff() {
				lg.Debugf("offline: skipping commit tick (backoff active)")
				s.saveCache()
				continue
			}
			if err := s.tryCommit(ctx, wc, username, password); err != nil {
				lg.Warnf("commit attempt failed: %v", err)
			}
			s.saveCache()
		}
	}
}

func (s *Service) shutdownDrain(wc, username, password string) {
	s.saveCache()
	drainCtx, cancel := context.WithTimeout(context.Background(), s.Rules.ShutdownTimeout)
	s.drain(drainCtx, wc, username, password)
	cancel()
	s.saveCache()
}

// runPoller periodically checks whether the server has new commits and triggers svn update.
func (s *Service) runPoller(ctx context.Context, wc, username, password string) {
	headRevPath := filepath.Join(wc, ".filees", "state", "head.rev")
	// Establish the revision baseline immediately. Without this, a fresh WC
	// that is already at HEAD reports revision 0 through IPC until the first
	// poll interval elapses (and historically never wrote head.rev at all).
	s.pollOnce(ctx, wc, username, password, headRevPath)
	ticker := time.NewTicker(s.Rules.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.isOfflineBackoff() {
				s.Logger.Debugf("poll: offline — skipping")
				continue
			}
			s.pollOnce(ctx, wc, username, password, headRevPath)
		}
	}
}

// pollOnce checks HEAD revision against local and runs svn update when behind.
func (s *Service) pollOnce(ctx context.Context, wc, username, password, headRevPath string) {
	headRev, err := s.Cli.Revision(ctx, s.RepoURL, username, password)
	if err != nil {
		if client.IsNetworkError(err) {
			s.goOffline()
		} else {
			s.Logger.Warnf("poll: HEAD revision: %v", err)
		}
		return
	}

	localRev, err := s.Cli.Revision(ctx, wc, username, password)
	if err != nil {
		s.Logger.Warnf("poll: local revision: %v", err)
		return
	}

	s.goOnline()

	if headRev <= localRev {
		if err := atomicWriteString(headRevPath, fmt.Sprintf("%d\n", localRev)); err != nil {
			s.Logger.Warnf("poll: persist local revision: %v", err)
		}
		return // already up to date
	}

	s.Logger.Infof("poll: HEAD r%d > local r%d — running svn update", headRev, localRev)
	out, err := s.Cli.Update(ctx, wc, username, password)
	if err != nil {
		if client.IsNetworkError(err) {
			s.goOffline()
		} else {
			s.Logger.Warnf("poll: svn update failed: %v\n%s", err, out)
			s.ErrSink.Emit(errmap.Classify(err))
		}
		return
	}

	s.ReconcileUpdateConflicts(ctx, wc, username, password, out)

	s.Logger.Infof("poll: updated to r%d", headRev)
	_ = atomicWriteString(headRevPath, fmt.Sprintf("%d\n", headRev))
	s.emit(contract.EvSyncCompleted, contract.SyncCompletedPayload{Revision: headRev})
}

func (s *Service) addEvent(ev watcher.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		if it.Op != watcher.Added && it.Op != watcher.Deleted {
			it.Op = watcher.Modified
		}
	case watcher.Renamed:
		it.Op = watcher.Renamed
		it.OldRel = ev.OldRel
	}
	// refresh Abs/IsDir if needed
	it.Abs = ev.Path
	it.IsDir = (ev.Type == watcher.EntryDir)
	it.ver++
}

type pendingEntry struct {
	item *stageItem
	ver  uint64
}

func (s *Service) tryCommit(ctx context.Context, wc, username, password string) error {
	return s.tryCommitMode(ctx, wc, username, password, false)
}

func (s *Service) tryCommitMode(ctx context.Context, wc, username, password string, force bool) error {
	s.mu.Lock()
	// snapshot and filter by latency & max batch
	now := time.Now()
	pending := make([]pendingEntry, 0, len(s.staging))
	for key, it := range s.staging {
		if it.Op == watcher.Added {
			if _, err := os.Stat(it.Abs); errors.Is(err, os.ErrNotExist) {
				// Added and removed before publication is a cancelled addition,
				// not a deletion that should reach SVN.
				delete(s.staging, key)
				continue
			}
			if !force && now.Sub(it.FirstSeen) < s.Rules.NewLatency {
				continue
			}
		}
		pending = append(pending, pendingEntry{it, it.ver})
	}
	s.mu.Unlock()

	if len(pending) == 0 {
		return nil
	}

	// size-adaptive interval: jeśli tiery są skonfigurowane, sprawdź czy minął wymagany odstęp
	if !force && len(s.Rules.SizeTiers) > 0 {
		var totalBytes int64
		for _, it := range pending {
			if it.item.Op != watcher.Deleted {
				if fi, err := os.Stat(it.item.Abs); err == nil {
					totalBytes += fi.Size()
				}
			}
		}
		required := s.Rules.effectiveInterval(totalBytes)
		if required > 0 && !s.lastCommit.IsZero() && time.Since(s.lastCommit) < required {
			remaining := time.Until(s.lastCommit.Add(required)).Round(time.Second)
			s.Logger.Debugf("size-adaptive: %.1f MiB — czekam jeszcze %s (wymagany interwał %s)",
				float64(totalBytes)/(1<<20), remaining, required)
			return nil
		}
	}

	// Stable, two-dimensional batch plan. Directories cost no payload and no
	// file slot; svn add uses --depth empty, so they cannot expand behind us.
	sort.Slice(pending, func(i, j int) bool { return pending[i].item.Rel < pending[j].item.Rel })
	pending = selectBatch(pending, s.Rules.MaxBatchFiles, s.Rules.MaxBatchBytes)

	// wstępne listy
	var addPaths, delPaths, commitPaths []string
	var renamedItems []*stageItem
	for _, it := range pending {
		switch it.item.Op {
		case watcher.Added:
			addPaths = append(addPaths, it.item.Rel)
			commitPaths = append(commitPaths, it.item.Rel)
		case watcher.Modified:
			commitPaths = append(commitPaths, it.item.Rel)
		case watcher.Deleted:
			delPaths = append(delPaths, it.item.Rel)
		case watcher.Renamed:
			renamedItems = append(renamedItems, it.item)
			commitPaths = append(commitPaths, it.item.OldRel, it.item.Rel)
		}
	}

	// --- FILTR STAGINGU PRZEZ `svn status` ---
	all := append(append([]string{}, addPaths...), delPaths...)
	for _, it := range renamedItems {
		all = append(all, it.OldRel, it.Rel)
	}
	all = dedup(all)
	st := s.statusMap(ctx, wc, username, password, all)

	// Rename detection is content-based and may refer to a source retained only
	// in an old manifest. If that source is no longer versioned, degrade safely
	// to Added instead of letting an impossible svn delete block the whole drain.
	validRenamed := renamedItems[:0]
	for _, it := range renamedItems {
		switch st[it.OldRel] {
		case "normal", "modified", "missing", "deleted":
			validRenamed = append(validRenamed, it)
		default:
			s.Logger.Debugf("rename source %s is not versioned; treating %s as added", it.OldRel, it.Rel)
			addPaths = append(addPaths, it.Rel)
		}
	}
	renamedItems = validRenamed

	// ADD: tylko istniejące i unversioned. The existence check is repeated
	// immediately before staging to close the debounce/status race window.
	toSvnAdd := make([]string, 0, len(addPaths))
	alreadyAdded := make([]string, 0, len(addPaths))
	for _, p := range addPaths {
		if _, err := os.Stat(filepath.Join(wc, filepath.FromSlash(p))); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				s.Logger.Debugf("cancel add %s (removed before publish)", p)
				s.removePendingIfUnchanged(p, pending)
				continue
			}
			s.Logger.Warnf("skip add %s (stat: %v)", p, err)
			continue
		}
		item := st[p]
		if item == "unversioned" || (item == "" && hasUnversionedAncestor(p, st)) {
			toSvnAdd = append(toSvnAdd, p)
		} else if item == "added" {
			alreadyAdded = append(alreadyAdded, p)
			s.Logger.Debugf("skip svn add %s (already staged)", p)
		} else {
			s.Logger.Debugf("skip add %s (status=%s)", p, item)
			s.removePendingIfUnchanged(p, pending)
		}
	}
	toSvnAdd = dedup(toSvnAdd)
	addPaths = dedup(append(toSvnAdd, alreadyAdded...))

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
			s.removePendingIfUnchanged(p, pending)
		}
	}
	delPaths = append(toSvnDelete, alreadyStaged...)

	// Rebuild from the filtered operation lists. Never retain a path merely
	// because it appeared in the pre-filter snapshot.
	commitPaths = commitPaths[:0]
	commitPaths = append(commitPaths, addPaths...)
	for _, pe := range pending {
		if pe.item.Op == watcher.Modified {
			commitPaths = append(commitPaths, pe.item.Rel)
		}
	}
	commitPaths = append(commitPaths, delPaths...)
	for _, it := range renamedItems {
		commitPaths = append(commitPaths, it.OldRel, it.Rel)
	}
	// --- KONIEC FILTRA ---

	if len(commitPaths) == 0 {
		return nil
	}

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
		if err != nil {
			return err
		}
		defer release()
	}
	if s.RepoMtx != nil {
		unlock, err := s.RepoMtx.Lock(ctx, s.RepoURL)
		if err != nil {
			return err
		}
		defer unlock()
	}

	// busy flag
	busy := filepath.Join(wc, ".filees", "state", "commit.busy")
	if err := atomicWriteString(busy, fmt.Sprintf("ts_start=%d\npid=%d\nrepo=%s\n", time.Now().Unix(), os.Getpid(), s.RepoURL)); err == nil {
		defer os.Remove(busy)
	}

	// staging
	if len(toSvnAdd) > 0 {
		if out, err := s.Cli.Add(ctx, wc, toSvnAdd, username, password); err != nil {
			return fmt.Errorf("svn add failed: %w\n%s", err, out)
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
			if client.IsNetworkError(err) {
				s.goOffline()
				return fmt.Errorf("svn lock: %w", err)
			}
			entry := errmap.Classify(err)
			s.Logger.Warnf("svn lock failed [%s]: %v\n%s", entry.Code, err, out)
			s.ErrSink.Emit(entry)
		}
	}

	// commit
	uid := s.UUID
	if uid == "" {
		uid = "unknown"
	}
	msg := fmt.Sprintf("Auto-commit by FileES client %s: %d paths", uid, len(commitPaths))
	out, err := s.Cli.Commit(ctx, wc, commitPaths, msg, username, password)
	if err != nil {
		entry := errmap.Classify(err)
		s.ErrSink.Emit(entry)
		if client.IsNetworkError(err) {
			s.goOffline()
		}
		return fmt.Errorf("svn commit: %w\n%s", err, out)
	}
	s.goOnline()
	s.lastCommit = time.Now()

	// head.rev + commit event
	if rev := parseRevision(out); rev != "" {
		head := filepath.Join(wc, ".filees", "state", "head.rev")
		_ = atomicWriteString(head, rev+"\n")
		if rev64, err := strconv.ParseInt(rev, 10, 64); err == nil {
			s.emit(contract.EvCommitCompleted, contract.CommitCompletedPayload{
				Revision: rev64, Paths: len(commitPaths),
			})
		}
	}

	// cleanup staging — only remove items unchanged since snapshot
	s.mu.Lock()
	for _, pe := range pending {
		if cur, ok := s.staging[pe.item.Rel]; ok && cur == pe.item && cur.ver == pe.ver {
			delete(s.staging, pe.item.Rel)
		}
	}
	s.mu.Unlock()
	return nil
}

func hasUnversionedAncestor(rel string, status map[string]string) bool {
	rel = strings.Trim(rel, "/")
	for {
		i := strings.LastIndex(rel, "/")
		if i < 0 {
			return false
		}
		rel = rel[:i]
		if status[rel] == "unversioned" {
			return true
		}
	}
}

func selectBatch(entries []pendingEntry, maxFiles int, maxBytes int64) []pendingEntry {
	out := make([]pendingEntry, 0, len(entries))
	files := 0
	var bytes int64
	for _, pe := range entries {
		count := 0
		size := int64(0)
		if !pe.item.IsDir {
			count = 1
			if pe.item.Op != watcher.Deleted {
				if fi, err := os.Stat(pe.item.Abs); err == nil {
					size = fi.Size()
				}
			}
		}
		if count > 0 && files >= maxFiles {
			break
		}
		if size > 0 && bytes > 0 && bytes+size > maxBytes {
			break
		}
		out = append(out, pe)
		files += count
		bytes += size
	}
	return out
}

func (s *Service) stagingBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, it := range s.staging {
		if it.IsDir || it.Op == watcher.Deleted {
			continue
		}
		if fi, err := os.Stat(it.Abs); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func (s *Service) stagingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.staging)
}

func (s *Service) drain(ctx context.Context, wc, username, password string) {
	for s.stagingLen() > 0 {
		before := s.stagingLen()
		if err := s.tryCommitMode(ctx, wc, username, password, true); err != nil {
			s.Logger.Warnf("shutdown drain stopped with %d pending entries: %v", before, err)
			return
		}
		after := s.stagingLen()
		if after >= before {
			s.Logger.Warnf("shutdown drain made no progress; preserving %d pending entries", after)
			return
		}
		if err := ctx.Err(); err != nil {
			s.Logger.Warnf("shutdown drain timeout with %d pending entries: %v", after, err)
			return
		}
	}
}

func (s *Service) removePendingIfUnchanged(rel string, pending []pendingEntry) {
	for _, pe := range pending {
		if pe.item.Rel != rel {
			continue
		}
		s.mu.Lock()
		if cur, ok := s.staging[rel]; ok && cur == pe.item && cur.ver == pe.ver {
			delete(s.staging, rel)
		}
		s.mu.Unlock()
		return
	}
}

func (s *Service) makeNotice(wc, title, body string) error {
	if s.Tickets == nil {
		return nil
	}
	uid := s.UUID
	if uid == "" {
		uid = "unknown"
	}
	_, err := s.Tickets.CreateNotice(wc, uid, title, body)
	return err
}

// --- commit cache ---

// cacheEntry is the JSON-serializable form of stageItem.
type cacheEntry struct {
	Rel       string    `json:"rel"`
	Abs       string    `json:"abs"`
	OldRel    string    `json:"old_rel,omitempty"`
	IsDir     bool      `json:"is_dir,omitempty"`
	Op        string    `json:"op"`
	FirstSeen time.Time `json:"first_seen"`
}

func opName(op watcher.OpType) string {
	switch op {
	case watcher.Added:
		return "added"
	case watcher.Modified:
		return "modified"
	case watcher.Deleted:
		return "deleted"
	case watcher.Renamed:
		return "renamed"
	default:
		return "modified"
	}
}

func opFromName(s string) watcher.OpType {
	switch s {
	case "added":
		return watcher.Added
	case "deleted":
		return watcher.Deleted
	case "renamed":
		return watcher.Renamed
	default:
		return watcher.Modified
	}
}

func (s *Service) loadCache() {
	data, err := os.ReadFile(s.cachePath)
	if err != nil {
		return
	} // no cache yet = normal on first run

	var entries []cacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		s.Logger.Warnf("commit cache: parse error: %v — starting fresh", err)
		return
	}

	s.mu.Lock()
	for _, e := range entries {
		s.staging[e.Rel] = &stageItem{
			Rel:       e.Rel,
			Abs:       e.Abs,
			OldRel:    e.OldRel,
			IsDir:     e.IsDir,
			Op:        opFromName(e.Op),
			FirstSeen: e.FirstSeen,
		}
	}
	s.mu.Unlock()

	if len(entries) > 0 {
		s.Logger.Infof("commit cache: resumed %d pending entries", len(entries))
	}
}

func (s *Service) saveCache() {
	if s.cachePath == "" {
		return
	}

	s.mu.Lock()
	entries := make([]cacheEntry, 0, len(s.staging))
	for _, it := range s.staging {
		entries = append(entries, cacheEntry{
			Rel:       it.Rel,
			Abs:       it.Abs,
			OldRel:    it.OldRel,
			IsDir:     it.IsDir,
			Op:        opName(it.Op),
			FirstSeen: it.FirstSeen,
		})
	}
	s.mu.Unlock()

	if err := atomicWriteJSONSlice(s.cachePath, entries); err != nil {
		s.Logger.Warnf("commit cache: save failed: %v", err)
	}
}

func atomicWriteJSONSlice(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

func atomicWriteString(path string, data string) error {
	d := filepath.Dir(path)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		return err
	}
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
