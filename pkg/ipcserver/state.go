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

	server           *Server // for auto-emitting state-change events
	id               string
	url              string
	localPath        string
	serverID         string
	access           string
	displayName      string
	attached         bool
	ownerRealmID     string
	attachmentPolicy string
	projectedState   string

	state        string // contract.State*
	connectivity string // contract.Conn*
	headRev      int64  // last HEAD seen by poller; 0 = unknown
	conflicts    int
	lastSyncAt   time.Time
	currentOp    *string

	// SVN operation funcs wired by main.go; nil until SetLockFuncs is called.
	lockFn   func(ctx context.Context, paths []string) (string, error)
	unlockFn func(ctx context.Context, paths []string) (string, error)
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
	headRev := rs.headRev
	conflicts := rs.conflicts
	lastSync := rs.lastSyncAt
	var currentOp *string
	if rs.currentOp != nil {
		value := *rs.currentOp
		currentOp = &value
	}
	wc := rs.localPath
	rs.mu.RUnlock()

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
		RepoID:           rs.id,
		ServerID:         rs.serverID,
		DisplayName:      displayName,
		Attached:         attached,
		Access:           access,
		OwnerRealmID:     ownerRealmID,
		AttachmentPolicy: attachmentPolicy,
		State:            state,
		Connectivity:     conn,
		LocalRevision:    localRev,
		HeadRevision:     headRev,
		Pending:          pending,
		Conflicts:        conflicts,
		CurrentOperation: currentOp,
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
	Op string `json:"op"`
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
