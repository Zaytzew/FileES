package ipcserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	contract "filees/pkg/contract/v1"
)

// RepoState holds the live runtime state of one repo, updated by the daemon.
// All fields are protected by mu; use the Set* methods from any goroutine.
type RepoState struct {
	mu sync.RWMutex

	server              *Server // for auto-emitting state-change events
	id                  string
	url                 string
	localPath           string
	serverID            string
	access              string
	displayName         string
	attached            bool
	ownerRealmID        string
	attachmentPolicy    string
	editingPolicy       string
	purpose             string
	projectedState      string
	serverDeleted       bool
	localCleanupPending bool
	retainUntil         string
	recoveryOperationID string
	recoveryAvailable   bool
	recoveryPending     bool
	cleanupError        string

	state        string // contract.State*
	connectivity string // contract.Conn*
	headRev      int64  // last HEAD seen by poller; 0 = unknown
	conflicts    int
	lastSyncAt   time.Time
	currentOp    *string
	cycle        contract.CycleStatus

	// SVN operation funcs wired by main.go; nil until SetLockFuncs is called.
	lockFn               func(ctx context.Context, paths []string) (string, error)
	unlockFn             func(ctx context.Context, paths []string) (string, error)
	reservationListFn    func(ctx context.Context) (ReservationSnapshot, error)
	reservationReleaseFn func(ctx context.Context, path, expectedToken string, confirmRisk bool) error
	recoveryStatsFn      func() contract.RecoveryStats
	workingCopySizeFn    func() (int64, bool)
	publishFn            func(ctx context.Context, comment string) (int64, error)
	noticeListFn         func() ([]contract.Notice, error)
	noticeAckFn          func(id string) error
}

func (rs *RepoState) ServerID() string {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.serverID
}

// SetProjection refreshes server-owned presentation and authorization fields
// while preserving local runtime counters and callbacks.
func (rs *RepoState) SetProjection(url, access string) {
	rs.mu.Lock()
	rs.url = url
	rs.access = access
	rs.mu.Unlock()
}

// SetEditingPolicy records the projected repository-wide editing policy so
// status can explain read-only files instead of leaving them unexplained. It
// is separate from SetProjectedMetadata to keep that already long positional
// signature from growing another string nobody can tell apart at a call site.
func (rs *RepoState) SetEditingPolicy(policy string) {
	rs.mu.Lock()
	rs.editingPolicy = policy
	rs.mu.Unlock()
}

func (rs *RepoState) SetPurpose(purpose string) {
	rs.mu.Lock()
	rs.purpose = purpose
	rs.mu.Unlock()
}

func (rs *RepoState) SetProjectedMetadata(displayName, url, access, projectedState, ownerRealmID, attachmentPolicy string, attached bool) {
	rs.mu.Lock()
	rs.displayName = displayName
	rs.url = url
	rs.access = access
	rs.attached = attached
	rs.ownerRealmID = ownerRealmID
	if attachmentPolicy == "" {
		attachmentPolicy = "optional"
	}
	rs.attachmentPolicy = attachmentPolicy
	rs.projectedState = projectedState
	if !attached {
		rs.localPath = ""
		rs.currentOp = nil
		rs.lockFn = nil
		rs.unlockFn = nil
		rs.reservationReleaseFn = nil
	}
	if projectedState != "active" {
		rs.state = projectedState
	} else if !attached {
		if attachmentPolicy == "required" {
			rs.state = contract.StatePolicyPending
		} else {
			rs.state = contract.StateUnattached
		}
	}
	rs.mu.Unlock()
}

// SetPendingLocalPath exposes a daemon-owned local folder while repository
// creation is still importing its initial snapshot. It deliberately does not
// mark the repository attached: no working-copy runtime or mutation actions
// may start before INITIAL_COMMIT succeeds.
func (rs *RepoState) SetPendingLocalPath(localPath string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.attached {
		return
	}
	rs.localPath = localPath
}

func (rs *RepoState) SetDeletionMetadata(serverDeleted, cleanupPending bool, retainUntil, recoveryOperationID string, recoveryAvailable, recoveryPending bool, cleanupError string) {
	rs.mu.Lock()
	rs.serverDeleted = serverDeleted
	rs.localCleanupPending = cleanupPending
	rs.retainUntil = retainUntil
	rs.recoveryOperationID = recoveryOperationID
	rs.recoveryAvailable = recoveryAvailable
	rs.recoveryPending = recoveryPending
	rs.cleanupError = cleanupError
	rs.mu.Unlock()
}

func (rs *RepoState) markDetached() {
	rs.mu.Lock()
	rs.attached = false
	rs.localPath = ""
	rs.currentOp = nil
	rs.lockFn = nil
	rs.unlockFn = nil
	rs.reservationReleaseFn = nil
	if rs.attachmentPolicy == "required" {
		rs.state = contract.StatePolicyPending
	} else {
		rs.state = contract.StateUnattached
	}
	rs.mu.Unlock()
}

func (rs *RepoState) ProjectedState() string {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.projectedState
}

// SetState transitions the repo to a new state constant (contract.State*).
// Emits EvRepoStateChanged if the state actually changed.
func (rs *RepoState) SetState(newState string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	old := rs.state
	if old == newState {
		return
	}
	rs.state = newState
	if rs.server != nil {
		// Publish before releasing mu so concurrent transitions are emitted in
		// the same causal order in which they mutate rs.state.
		rs.server.Emit(rs.server.NewRepoEvent(rs.id, contract.EvRepoStateChanged,
			contract.RepoStateChangedPayload{OldState: old, NewState: newState}))
	}
}

// SetConnectivity sets the connectivity label ("online" or "offline").
func (rs *RepoState) SetConnectivity(c string) {
	rs.mu.Lock()
	rs.connectivity = c
	rs.mu.Unlock()
}

// SetHeadRev records the latest HEAD revision seen by the poller.
func (rs *RepoState) SetHeadRev(rev int64) {
	rs.mu.Lock()
	rs.headRev = rev
	rs.mu.Unlock()
}

// SetConflicts records the number of unresolved conflicts.
func (rs *RepoState) SetConflicts(n int) {
	rs.mu.Lock()
	rs.conflicts = n
	rs.mu.Unlock()
}

// SetLastSyncAt records the time of the last successful sync.
func (rs *RepoState) SetLastSyncAt(t time.Time) {
	rs.mu.Lock()
	rs.lastSyncAt = t
	rs.mu.Unlock()
}

// SetCurrentOp sets a short description of the in-progress operation, or nil.
func (rs *RepoState) SetCurrentOp(op *string) {
	rs.mu.Lock()
	if op == nil {
		rs.currentOp = nil
	} else {
		value := *op
		rs.currentOp = &value
	}
	rs.mu.Unlock()
}

// SetCycle records the daemon-owned runtime cadence and emits an invalidation
// event so subscribers can refresh it without inventing a local scheduler.
func (rs *RepoState) SetCycle(cycle contract.CycleStatus) {
	rs.mu.Lock()
	changed := rs.cycle != cycle
	rs.cycle = cycle
	srv := rs.server
	id := rs.id
	rs.mu.Unlock()
	if changed && srv != nil {
		srv.Emit(srv.NewRepoEvent(id, contract.EvRepoCycleChanged, cycle))
	}
}

// SetPublishFunc wires the shouting-commit publisher for this working copy.
func (rs *RepoState) SetPublishFunc(fn func(ctx context.Context, comment string) (int64, error)) {
	rs.mu.Lock()
	rs.publishFn = fn
	rs.mu.Unlock()
}

// SetNoticeFuncs wires the local shout inbox for this working copy.
func (rs *RepoState) SetNoticeFuncs(listFn func() ([]contract.Notice, error), ackFn func(id string) error) {
	rs.mu.Lock()
	rs.noticeListFn = listFn
	rs.noticeAckFn = ackFn
	rs.mu.Unlock()
}

func (rs *RepoState) Publish(ctx context.Context, comment string) (int64, error) {
	rs.mu.RLock()
	fn := rs.publishFn
	access := rs.access
	attached := rs.attached
	rs.mu.RUnlock()
	if !attached {
		return 0, fmt.Errorf("publish not available for detached repo %s", rs.id)
	}
	if access != contract.AccessReadWrite {
		return 0, fmt.Errorf("REPO_READ_ONLY: repo %s is read-only", rs.id)
	}
	if fn == nil {
		return 0, fmt.Errorf("publish not available for repo %s", rs.id)
	}
	return fn(ctx, comment)
}

func (rs *RepoState) Notices() ([]contract.Notice, error) {
	rs.mu.RLock()
	fn := rs.noticeListFn
	rs.mu.RUnlock()
	if fn == nil {
		return nil, nil
	}
	return fn()
}

func (rs *RepoState) EmitEvent(evType string, payload any) {
	rs.mu.RLock()
	srv := rs.server
	id := rs.id
	rs.mu.RUnlock()
	if srv == nil {
		return
	}
	srv.Emit(srv.NewRepoEvent(id, evType, payload))
}

func (rs *RepoState) AckNotice(id string) error {
	rs.mu.RLock()
	fn := rs.noticeAckFn
	rs.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(id)
}

// SetRecoveryStatsFunc wires a live reader of the commit service's
// crash-recovery counters into this RepoState; nil until called, in which
// case Snapshot reports the zero value.
func (rs *RepoState) SetRecoveryStatsFunc(fn func() contract.RecoveryStats) {
	rs.mu.Lock()
	rs.recoveryStatsFn = fn
	rs.mu.Unlock()
}

// SetWorkingCopySizeFunc wires the watcher's buffered manifest total into the
// status projection. The callback must not walk the filesystem.
func (rs *RepoState) SetWorkingCopySizeFunc(fn func() (int64, bool)) {
	rs.mu.Lock()
	rs.workingCopySizeFn = fn
	rs.mu.Unlock()
}

// SetLockFuncs wires the SVN lock and unlock operations into this RepoState.
// Both funcs receive absolute file paths; they handle WC-relative conversion internally.
func (rs *RepoState) SetLockFuncs(
	lockFn func(ctx context.Context, paths []string) (string, error),
	unlockFn func(ctx context.Context, paths []string) (string, error),
) {
	rs.mu.Lock()
	rs.lockFn = lockFn
	rs.unlockFn = unlockFn
	rs.mu.Unlock()
}

// ReservationSnapshot is what a wired listing function returns. It carries
// whatever freshness classification applies — fresh, a replayed remote stale
// artifact, an offline local mirror, or a total unknown (see
// pkg/reservation/v1.Result, the wire shape the remote serving-state worker
// answers with) — verbatim. RepoState and the IPC handler built on top of
// it never invent their own freshness judgement; they only relay one.
type ReservationSnapshot struct {
	Reservations []contract.Reservation
	Stale        bool
	// Offline means Reservations came from the desktop client's last local
	// mirror because the independent state SSH lane could not reach the
	// server. It is distinct from Stale, which is explicitly classified by
	// the remote serving-state worker itself.
	Offline bool
	// Unknown means the source has neither fresh data nor any prior
	// artifact to fall back to. Reservations must be empty; callers must
	// never treat Unknown as a confirmed zero.
	Unknown    bool
	AsOf       time.Time
	Generation string
}

// SetReservationFuncs wires the server-menu reservation inventory to this
// working copy.  The list callback may be present on read-only attachments;
// the release callback is deliberately nil there.
func (rs *RepoState) SetReservationFuncs(
	listFn func(ctx context.Context) (ReservationSnapshot, error),
	releaseFn func(ctx context.Context, path, expectedToken string, confirmRisk bool) error,
) {
	rs.mu.Lock()
	rs.reservationListFn = listFn
	rs.reservationReleaseFn = releaseFn
	rs.mu.Unlock()
}

// SetReservationListFunc replaces only the presentation source. The source
// remains valid for a projected repository without a local working copy;
// attaching and detaching a WC must not make a server-owned lock disappear.
func (rs *RepoState) SetReservationListFunc(fn func(ctx context.Context) (ReservationSnapshot, error)) {
	rs.mu.Lock()
	rs.reservationListFn = fn
	rs.mu.Unlock()
}

// SetReservationReleaseFunc replaces only the local mutation callback. It is
// cleared on detach while the server-owned listing callback remains wired.
func (rs *RepoState) SetReservationReleaseFunc(fn func(ctx context.Context, path, expectedToken string, confirmRisk bool) error) {
	rs.mu.Lock()
	rs.reservationReleaseFn = fn
	rs.mu.Unlock()
}

// ListReservations returns the last data known for this one attached
// working copy, exactly as its wired source classified it. An unwired or
// detached repo reports ReservationSnapshot{Unknown: true}, never a silent
// empty-but-confirmed list.
func (rs *RepoState) ListReservations(ctx context.Context) (ReservationSnapshot, error) {
	rs.mu.RLock()
	fn := rs.reservationListFn
	rs.mu.RUnlock()
	if fn == nil {
		return ReservationSnapshot{Unknown: true}, nil
	}
	return fn(ctx)
}

// ReleaseReservation performs the token-fenced release supplied by the daemon
// runtime.  It is distinct from the legacy repo.unlock picker path: callers
// can only target a row they just obtained from repo.reservation_list.
func (rs *RepoState) ReleaseReservation(ctx context.Context, path, expectedToken string, confirmRisk bool) error {
	rs.mu.RLock()
	fn := rs.reservationReleaseFn
	access := rs.access
	attached := rs.attached
	rs.mu.RUnlock()
	if !attached {
		return fmt.Errorf("reservation release not available for detached repo %s", rs.id)
	}
	if access != contract.AccessReadWrite {
		return fmt.Errorf("REPO_READ_ONLY: repo %s is read-only", rs.id)
	}
	if fn == nil {
		return fmt.Errorf("reservation release not available for repo %s", rs.id)
	}
	return fn(ctx, path, expectedToken, confirmRisk)
}

// Lock calls svn lock for the given absolute paths via the wired lockFn.
func (rs *RepoState) Lock(ctx context.Context, paths []string) (string, error) {
	rs.mu.RLock()
	fn := rs.lockFn
	access := rs.access
	rs.mu.RUnlock()
	if access != contract.AccessReadWrite {
		return "", fmt.Errorf("REPO_READ_ONLY: repo %s is read-only", rs.id)
	}
	if fn == nil {
		return "", fmt.Errorf("lock not available for repo %s", rs.id)
	}
	return fn(ctx, paths)
}

// Unlock calls svn unlock for the given absolute paths via the wired unlockFn.
func (rs *RepoState) Unlock(ctx context.Context, paths []string) (string, error) {
	rs.mu.RLock()
	fn := rs.unlockFn
	access := rs.access
	rs.mu.RUnlock()
	if access != contract.AccessReadWrite {
		return "", fmt.Errorf("REPO_READ_ONLY: repo %s is read-only", rs.id)
	}
	if fn == nil {
		return "", fmt.Errorf("unlock not available for repo %s", rs.id)
	}
	return fn(ctx, paths)
}

// Snapshot builds a contract.RepoStatus from live state and on-disk files.
// Reading head.rev and cache.json from disk keeps the IPC handler decoupled from
// the engine's internal structs while still serving fresh data.
func (rs *RepoState) Snapshot() contract.RepoStatus {
	rs.mu.RLock()
	state := rs.state
	conn := rs.connectivity
	access := rs.access
	displayName := rs.displayName
	attached := rs.attached
	ownerRealmID := rs.ownerRealmID
	attachmentPolicy := rs.attachmentPolicy
	editingPolicy := rs.editingPolicy
	purpose := rs.purpose
	headRev := rs.headRev
	conflicts := rs.conflicts
	lastSync := rs.lastSyncAt
	cycle := rs.cycle
	var currentOp *string
	if rs.currentOp != nil {
		value := *rs.currentOp
		currentOp = &value
	}
	wc := rs.localPath
	recoveryStatsFn := rs.recoveryStatsFn
	workingCopySizeFn := rs.workingCopySizeFn
	rs.mu.RUnlock()

	var recovery contract.RecoveryStats
	if recoveryStatsFn != nil {
		recovery = recoveryStatsFn()
	}
	workingCopyBytes, workingCopySizeKnown := int64(0), false
	if workingCopySizeFn != nil {
		workingCopyBytes, workingCopySizeKnown = workingCopySizeFn()
	}

	localRev := int64(0)
	pending := contract.PendingStats{}
	if attached && wc != "" {
		localRev = readRevFile(filepath.Join(wc, ".filees", "state", "head.rev"))
		pending = readPendingStats(filepath.Join(wc, ".filees", "commit_cache", "cache.json"))
	}
	if headRev == 0 {
		headRev = localRev
	}

	snap := contract.RepoStatus{
		RepoID:               rs.id,
		ServerID:             rs.serverID,
		DisplayName:          displayName,
		Attached:             attached,
		Access:               access,
		OwnerRealmID:         ownerRealmID,
		AttachmentPolicy:     attachmentPolicy,
		EditingPolicy:        editingPolicy,
		State:                state,
		Connectivity:         conn,
		LocalRevision:        localRev,
		HeadRevision:         headRev,
		WorkingCopyBytes:     workingCopyBytes,
		WorkingCopySizeKnown: workingCopySizeKnown,
		Pending:              pending,
		Conflicts:            conflicts,
		CurrentOperation:     currentOp,
		Cycle:                cycle,
		Recovery:             recovery,
		Purpose:              purpose,
	}
	if !lastSync.IsZero() {
		snap.LastSyncAt = lastSync.UTC().Format(time.RFC3339)
	}
	return snap
}

// Summary returns the minimal RepoSummary used in repo.list.
func (rs *RepoState) Summary() contract.RepoSummary {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return contract.RepoSummary{
		ID:               rs.id,
		ServerID:         rs.serverID,
		DisplayName:      rs.displayName,
		Attached:         rs.attached,
		Access:           rs.access,
		URL:              rs.url,
		LocalPath:        rs.localPath,
		State:            rs.state,
		OwnerRealmID:     rs.ownerRealmID,
		AttachmentPolicy: rs.attachmentPolicy,
		ServerDeleted:    rs.serverDeleted, LocalCleanupPending: rs.localCleanupPending,
		RetainUntil: rs.retainUntil, RecoveryOperationID: rs.recoveryOperationID,
		RecoveryAvailable: rs.recoveryAvailable, RecoveryPending: rs.recoveryPending, CleanupError: rs.cleanupError,
		Purpose: rs.purpose,
	}
}

// --- file-reading helpers (daemon reads its own state files) ---

// readRevFile reads the numeric revision from head.rev.
func readRevFile(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// cacheEntry mirrors the minimal shape of commit_cache/cache.json entries.
type cacheEntry struct {
	Abs   string `json:"abs"`
	IsDir bool   `json:"is_dir,omitempty"`
	Op    string `json:"op"`
}

// readPendingStats counts added/modified/deleted entries in cache.json.
func readPendingStats(path string) contract.PendingStats {
	data, err := os.ReadFile(path)
	if err != nil {
		return contract.PendingStats{}
	}
	var entries []cacheEntry
	if json.Unmarshal(data, &entries) != nil {
		return contract.PendingStats{}
	}
	var ps contract.PendingStats
	for _, e := range entries {
		switch e.Op {
		case "added":
			ps.Added++
		case "modified":
			ps.Modified++
		case "deleted":
			ps.Deleted++
		}
		if e.Op != "deleted" && !e.IsDir && filepath.IsAbs(e.Abs) {
			if info, err := os.Stat(e.Abs); err == nil && info.Mode().IsRegular() {
				ps.TotalBytes += info.Size()
			}
		}
	}
	return ps
}

// readLastErrors reads the last n non-empty lines from errors.jsonl.
func readLastErrors(logPath string, n int) []string {
	f, err := os.Open(logPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}
