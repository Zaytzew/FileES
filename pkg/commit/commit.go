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
	"sync/atomic"
	"time"

	"filees/pkg/activity"
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
	NeedsLock         bool           // attach svn:needs-lock to newly added regular files
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
	Activity interface {
		Record(activity.Entry) error
	}
	// OnConnectivity is called (async) when online/offline state changes.
	// Argument is "online" or "offline". May be nil.
	OnConnectivity func(string)
	// Status callbacks keep the public daemon snapshot current without exposing
	// commit internals to the IPC package.
	OnHeadRevision     func(int64)
	OnLastSync         func(time.Time)
	OnConflicts        func(int)
	OnCurrentOperation func(*string)
	// BeginPublish verifies edit-passport fencing and freezes lock mutation until
	// the returned release function is called after the publication attempt.
	BeginPublish func(context.Context, []string) (func(), error)
	// OnPathActivity resets close-grace after any further watcher activity.
	OnPathActivity func(string)
	// OnPathsPublished starts close-grace after a confirmed central commit.
	OnPathsPublished func([]string)
	// OnPathsRemoved forgets passports whose repository paths were deleted by a
	// confirmed commit (including rename sources).
	OnPathsRemoved func([]string)
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

	cacheResumed    atomic.Int64
	alreadyAccepted atomic.Int64
	commitBatches   atomic.Int64
}

// RecoveryStats is a point-in-time diagnostic snapshot; it is not an IPC type.
type RecoveryStats struct {
	CacheResumed    int64
	AlreadyAccepted int64
	CommitBatches   int64
}

func (s *Service) RecoveryStats() RecoveryStats {
	return RecoveryStats{
		CacheResumed:    s.cacheResumed.Load(),
		AlreadyAccepted: s.alreadyAccepted.Load(),
		CommitBatches:   s.commitBatches.Load(),
	}
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

func (s *Service) setOperation(name string) func() {
	if s.OnCurrentOperation == nil {
		return func() {}
	}
	s.OnCurrentOperation(&name)
	return func() { s.OnCurrentOperation(nil) }
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
func (s *Service) Run(ctx context.Context, repoID, wc string, events <-chan watcher.Event) {
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
		go s.runPoller(ctx, wc)
	}

	for {
		select {
		case <-ctx.Done():
			// The watcher closes its channel after its final state save. Consume
			// everything it emitted before beginning the bounded shutdown drain.
			for ev := range events {
				s.acceptEvent(ev)
			}
			s.shutdownDrain(wc)
			lg.Infof("commit service stop")
			return
		case ev, ok := <-events:
			if !ok {
				s.shutdownDrain(wc)
				lg.Infof("commit service stop")
				return
			}
			s.acceptEvent(ev)
			stagedBytes, watermarkEligible := s.watermarkState(time.Now())
			if stagedBytes >= s.Rules.BacklogFlushBytes && watermarkEligible && !s.isOfflineBackoff() {
				// A high watermark may accelerate stable modifications, but it must
				// never bypass NewLatency for a newly-created file. Large files can
				// cross the watermark while they are still being written or just
				// before an application atomically renames its temporary path.
				if err := s.tryCommitMode(ctx, wc, false); err != nil {
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
			if err := s.tryCommit(ctx, wc); err != nil {
				lg.Warnf("commit attempt failed: %v", err)
			}
			s.saveCache()
		}
	}
}

func (s *Service) acceptEvent(ev watcher.Event) {
	s.recordActivity(ev.Rel, ev.Op, activity.Detected, 0, "")
	s.addEvent(ev)
	s.saveCache()
	s.recordActivity(ev.Rel, ev.Op, activity.Pending, 0, "")
}

func (s *Service) recordActivity(rel string, op watcher.OpType, stage activity.Stage, revision int64, errorID string) {
	if s.Activity == nil || s.repoID == "" || rel == "" {
		return
	}
	kind := activity.Modified
	switch op {
	case watcher.Added:
		kind = activity.Added
	case watcher.Deleted:
		kind = activity.Deleted
	case watcher.Renamed:
		kind = activity.Renamed
	}
	if err := s.Activity.Record(activity.Entry{RepoID: s.repoID, Path: filepath.ToSlash(rel), Kind: kind, Stage: stage, Revision: revision, ErrorID: errorID}); err != nil {
		s.Logger.Warnf("activity journal: %v", err)
		return
	}
	s.emit(contract.EvActivityChanged, nil)
}

func (s *Service) shutdownDrain(wc string) {
	s.saveCache()
	drainCtx, cancel := context.WithTimeout(context.Background(), s.Rules.ShutdownTimeout)
	s.drain(drainCtx, wc)
	cancel()
	s.saveCache()
}

// runPoller periodically checks whether the server has new commits and triggers svn update.
func (s *Service) runPoller(ctx context.Context, wc string) {
	headRevPath := filepath.Join(wc, ".filees", "state", "head.rev")
	// Establish the revision baseline immediately. Without this, a fresh WC
	// that is already at HEAD reports revision 0 through IPC until the first
	// poll interval elapses (and historically never wrote head.rev at all).
	s.pollOnce(ctx, wc, headRevPath)
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
			s.pollOnce(ctx, wc, headRevPath)
		}
	}
}

// pollOnce checks HEAD revision against local and runs svn update when behind.
func (s *Service) pollOnce(ctx context.Context, wc, headRevPath string) {
	headRev, err := s.Cli.Revision(ctx, s.RepoURL)
	if err != nil {
		if client.IsNetworkError(err) {
			s.goOffline()
		} else {
			s.Logger.Warnf("poll: HEAD revision: %v", err)
		}
		return
	}

	localRev, err := s.Cli.Revision(ctx, wc)
	if err != nil {
		s.Logger.Warnf("poll: local revision: %v", err)
		return
	}

	s.goOnline()

	if headRev <= localRev {
		if err := atomicWriteString(headRevPath, fmt.Sprintf("%d\n", localRev)); err != nil {
			s.Logger.Warnf("poll: persist local revision: %v", err)
		}
		if s.OnHeadRevision != nil {
			s.OnHeadRevision(localRev)
		}
		return // already up to date
	}

	status, err := s.Cli.Status(ctx, wc, nil)
	if err != nil {
		s.Logger.Warnf("poll: svn status before update failed: %v — update deferred", err)
		return
	}
	if client.HasMissingPaths(status) {
		s.Logger.Infof("poll: update deferred while working copy contains local removals")
		return
	}

	s.Logger.Infof("poll: HEAD r%d > local r%d — running svn update", headRev, localRev)
	done := s.setOperation("sync")
	defer done()
	out, err := s.Cli.Update(ctx, wc)
	if err != nil {
		if client.IsNetworkError(err) {
			s.goOffline()
		} else {
			s.Logger.Warnf("poll: svn update failed: %v\n%s", err, out)
			s.ErrSink.Emit(errmap.Classify(err))
		}
		return
	}

	s.ReconcileUpdateConflicts(ctx, wc, out)

	s.Logger.Infof("poll: updated to r%d", headRev)
	_ = atomicWriteString(headRevPath, fmt.Sprintf("%d\n", headRev))
	if s.OnHeadRevision != nil {
		s.OnHeadRevision(headRev)
	}
	if s.OnLastSync != nil {
		s.OnLastSync(time.Now())
	}
	s.emit(contract.EvSyncCompleted, contract.SyncCompletedPayload{Revision: headRev})
}

func (s *Service) addEvent(ev watcher.Event) {
	if s.OnPathActivity != nil {
		s.OnPathActivity(ev.Path)
	}
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

func (s *Service) tryCommit(ctx context.Context, wc string) error {
	return s.tryCommitMode(ctx, wc, false)
}

func (s *Service) tryCommitMode(ctx context.Context, wc string, force bool) error {
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
	st, err := s.statusMap(ctx, wc, all)
	if err != nil {
		// Status is the proof used to classify every pending operation. An empty
		// map after a failed status call is not proof that any path is complete.
		// Preserve the whole batch for retry instead of manufacturing a no-op.
		return err
	}

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
	var alreadyPublished []string
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
		} else if item == "normal" {
			// Already published — by an earlier run of this process, by the
			// initial-import path, or by a restart that raced this one. The
			// activity journal must be told explicitly: nothing else ever
			// advances an entry past Pending except an in-process commit
			// success below, so without this call it would hang there forever.
			s.Logger.Debugf("skip add %s (status=%s)", p, item)
			s.alreadyAccepted.Add(1)
			s.removePendingIfUnchanged(p, pending)
			alreadyPublished = append(alreadyPublished, p)
		} else {
			// Empty and unknown statuses are inconclusive. In particular this can
			// happen when a never-published file is renamed while its surrounding
			// directory is being split across batches. Never acknowledge an add
			// merely because SVN did not describe it in this status response.
			s.Logger.Debugf("defer add %s (status=%s)", p, item)
		}
	}
	toSvnAdd = dedup(toSvnAdd)
	addPaths = dedup(append(toSvnAdd, alreadyAdded...))
	if len(alreadyPublished) > 0 && s.Activity != nil {
		if rev, err := s.Cli.Revision(ctx, wc); err != nil {
			s.Logger.Warnf("already-published activity: read WC revision: %v", err)
		} else if rev > 0 {
			for _, p := range alreadyPublished {
				s.recordActivity(p, watcher.Added, activity.Published, rev, "")
			}
		}
	}

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
	// svn add --parents schedules missing ancestors, but svn commit does not
	// automatically include those scheduled directories when only a child is
	// named. Close every added destination over its parents so a file-limited
	// batch is still a valid repository transaction.
	for _, p := range append(append([]string{}, addPaths...), renameDestinations(renamedItems)...) {
		commitPaths = append(commitPaths, parentPaths(p)...)
	}
	commitPaths = dedup(commitPaths)
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

	var protected []string
	for _, pe := range pending {
		if pe.item.Op == watcher.Modified {
			protected = append(protected, pe.item.Abs)
		}
	}
	for _, rel := range delPaths {
		for _, pe := range pending {
			if pe.item.Rel == rel && !pe.item.IsDir {
				protected = append(protected, pe.item.Abs)
				break
			}
		}
	}
	for _, it := range renamedItems {
		if !it.IsDir && it.OldRel != "" {
			protected = append(protected, filepath.Join(wc, filepath.FromSlash(it.OldRel)))
		}
	}
	endPublish := func() {}
	if s.BeginPublish != nil && len(protected) > 0 {
		var beginErr error
		endPublish, beginErr = s.BeginPublish(ctx, dedup(protected))
		if beginErr != nil {
			return fmt.Errorf("edit passport authorization: %w", beginErr)
		}
		defer endPublish()
	}

	// busy flag
	doneOperation := s.setOperation("commit")
	defer doneOperation()
	busy := filepath.Join(wc, ".filees", "state", "commit.busy")
	if err := atomicWriteString(busy, fmt.Sprintf("ts_start=%d\npid=%d\nrepo=%s\n", time.Now().Unix(), os.Getpid(), s.RepoURL)); err == nil {
		defer os.Remove(busy)
	}

	// staging
	if len(toSvnAdd) > 0 {
		if out, err := s.Cli.Add(ctx, wc, toSvnAdd); err != nil {
			return fmt.Errorf("svn add failed: %w\n%s", err, out)
		}
	}
	if s.Rules.NeedsLock {
		if files := regularPaths(wc, toSvnAdd); len(files) > 0 {
			if out, err := s.Cli.PropSet(ctx, wc, "svn:needs-lock", "*", files); err != nil {
				return fmt.Errorf("svn propset needs-lock: %w\n%s", err, out)
			}
		}
	}
	// A directory can become mixed-revision after earlier batches commit
	// changes to its descendants. Refresh only the directory node before
	// scheduling its removal. --depth empty cannot restore missing children.
	var deleteDirs []string
	for _, p := range toSvnDelete {
		if pendingPathIsDir(p, pending) {
			deleteDirs = append(deleteDirs, p)
		}
	}
	if len(deleteDirs) > 0 {
		if out, err := s.Cli.UpdateDepthEmpty(ctx, wc, deleteDirs); err != nil {
			return fmt.Errorf("svn update deleted directories: %w\n%s", err, out)
		}
	}
	if len(toSvnDelete) > 0 {
		if out, err := s.Cli.Delete(ctx, wc, toSvnDelete); err != nil {
			s.Logger.Warnf("svn delete failed: %v\n%s", err, out)
		}
	}
	for _, it := range renamedItems {
		if out, err := s.Cli.Delete(ctx, wc, []string{it.OldRel}); err != nil {
			s.Logger.Warnf("svn delete (rename src) %s: %v\n%s", it.OldRel, err, out)
		}
		if out, err := s.Cli.Add(ctx, wc, []string{it.Rel}); err != nil {
			s.Logger.Warnf("svn add (rename dst) %s: %v\n%s", it.Rel, err, out)
		}
		if s.Rules.NeedsLock && !it.IsDir {
			if out, err := s.Cli.PropSet(ctx, wc, "svn:needs-lock", "*", []string{it.Rel}); err != nil {
				return fmt.Errorf("svn propset rename destination: %w\n%s", err, out)
			}
		}
	}
	if s.Rules.LockFirst && len(commitPaths) > 0 {
		if out, err := s.Cli.Lock(ctx, wc, commitPaths); err != nil {
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
	commitSet := make(map[string]struct{}, len(commitPaths))
	for _, rel := range commitPaths {
		commitSet[rel] = struct{}{}
	}
	var activityItems []*stageItem
	for _, pe := range pending {
		if _, ok := commitSet[pe.item.Rel]; ok && !pe.item.IsDir {
			activityItems = append(activityItems, pe.item)
			s.recordActivity(pe.item.Rel, pe.item.Op, activity.Publishing, 0, "")
		}
	}
	var out string
	if s.Rules.NeedsLock {
		out, err = s.Cli.CommitKeepLocks(ctx, wc, commitPaths, msg)
	} else {
		out, err = s.Cli.Commit(ctx, wc, commitPaths, msg)
	}
	if err != nil {
		ts := time.Now()
		entry := errmap.Classify(err)
		s.ErrSink.EmitAt(ts, entry)
		if client.IsNetworkError(err) {
			// Transient: the same batch will be retried once connectivity
			// returns, so the activity feed should keep showing it as queued.
			for _, item := range activityItems {
				s.recordActivity(item.Rel, item.Op, activity.Pending, 0, "")
			}
			s.goOffline()
		} else {
			// Not a connectivity problem: retrying the identical batch will
			// fail identically. Surface it as failed with a link to the
			// error.list entry instead of leaving it looking merely queued.
			errorID := ts.UTC().Format(time.RFC3339) + ":" + string(entry.Code)
			for _, item := range activityItems {
				s.recordActivity(item.Rel, item.Op, activity.Failed, 0, errorID)
			}
		}
		return fmt.Errorf("svn commit: %w\n%s", err, out)
	}
	s.goOnline()
	s.lastCommit = time.Now()
	s.commitBatches.Add(1)

	// head.rev + commit event
	var confirmedRevision int64
	if rev := parseRevision(out); rev != "" {
		head := filepath.Join(wc, ".filees", "state", "head.rev")
		_ = atomicWriteString(head, rev+"\n")
		if rev64, err := strconv.ParseInt(rev, 10, 64); err == nil {
			confirmedRevision = rev64
			if s.OnHeadRevision != nil {
				s.OnHeadRevision(rev64)
			}
			if s.OnLastSync != nil {
				s.OnLastSync(time.Now())
			}
			s.emit(contract.EvCommitCompleted, contract.CommitCompletedPayload{
				Revision: rev64, Paths: len(commitPaths),
			})
		}
	}
	if confirmedRevision > 0 {
		for _, item := range activityItems {
			s.recordActivity(item.Rel, item.Op, activity.Published, confirmedRevision, "")
		}
	} else {
		for _, item := range activityItems {
			s.recordActivity(item.Rel, item.Op, activity.Pending, 0, "")
		}
	}

	// Cleanup staging only for operations represented in the successful commit.
	// `pending` also contains paths filtered or deferred above; deleting the
	// whole snapshot here would lose those paths whenever another member of the
	// same batch commits successfully.
	committed := make(map[string]struct{}, len(addPaths)+len(delPaths)+len(renamedItems))
	for _, p := range addPaths {
		committed[p] = struct{}{}
	}
	for _, p := range delPaths {
		committed[p] = struct{}{}
	}
	for _, it := range renamedItems {
		committed[it.Rel] = struct{}{}
	}
	for _, pe := range pending {
		if pe.item.Op == watcher.Modified {
			committed[pe.item.Rel] = struct{}{}
		}
	}

	s.mu.Lock()
	for _, pe := range pending {
		if _, ok := committed[pe.item.Rel]; !ok {
			continue
		}
		if cur, ok := s.staging[pe.item.Rel]; ok && cur == pe.item && cur.ver == pe.ver {
			delete(s.staging, pe.item.Rel)
		}
	}
	s.mu.Unlock()
	if s.OnPathsPublished != nil {
		var published []string
		for _, pe := range pending {
			if pe.item.Op == watcher.Modified {
				published = append(published, pe.item.Abs)
			}
		}
		s.OnPathsPublished(published)
	}
	if s.OnPathsRemoved != nil {
		var removed []string
		for _, rel := range delPaths {
			for _, pe := range pending {
				if pe.item.Rel == rel && !pe.item.IsDir {
					removed = append(removed, pe.item.Abs)
					break
				}
			}
		}
		for _, item := range renamedItems {
			if !item.IsDir && item.OldRel != "" {
				removed = append(removed, filepath.Join(wc, filepath.FromSlash(item.OldRel)))
			}
		}
		s.OnPathsRemoved(removed)
	}
	return nil
}

func regularPaths(wc string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if info, err := os.Stat(filepath.Join(wc, filepath.FromSlash(p))); err == nil && info.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
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

func renameDestinations(items []*stageItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Rel)
	}
	return out
}

func parentPaths(rel string) []string {
	rel = strings.Trim(strings.TrimSpace(rel), "/")
	var out []string
	for {
		i := strings.LastIndex(rel, "/")
		if i < 0 {
			break
		}
		rel = rel[:i]
		out = append(out, rel)
	}
	return out
}

func pendingPathIsDir(rel string, entries []pendingEntry) bool {
	for _, pe := range entries {
		if pe.item.Rel == rel {
			return pe.item.IsDir
		}
	}
	return false
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

// watermarkState computes the payload and whether a non-forced attempt can
// make progress. An immature Added entry may cross the byte watermark while it
// is still being written, but it must not cause a status/commit attempt until
// it matures or another immediately actionable operation is present.
func (s *Service) watermarkState(now time.Time) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	eligible := false
	for _, it := range s.staging {
		if it.Op != watcher.Added || now.Sub(it.FirstSeen) >= s.Rules.NewLatency {
			eligible = true
		}
		if it.IsDir || it.Op == watcher.Deleted {
			continue
		}
		if fi, err := os.Stat(it.Abs); err == nil {
			total += fi.Size()
		}
	}
	return total, eligible
}

func (s *Service) stagingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.staging)
}

func (s *Service) drain(ctx context.Context, wc string) {
	for s.stagingLen() > 0 {
		before := s.stagingLen()
		if err := s.tryCommitMode(ctx, wc, true); err != nil {
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
		s.cacheResumed.Add(int64(len(entries)))
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
func (s *Service) statusMap(ctx context.Context, wc string, paths []string) (map[string]string, error) {
	out := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return out, nil
	}
	st, err := s.Cli.Status(ctx, wc, paths)
	if err != nil {
		s.Logger.Warnf("svn status failed: %v", err)
		return nil, fmt.Errorf("svn status pending paths: %w", err)
	}
	for _, e := range st {
		// Upewnij się, że mamy POSIX (watcher emituje REL w POSIX)
		p := strings.ReplaceAll(e.Path, "\\", "/")
		out[p] = e.Item
	}
	return out, nil
}
