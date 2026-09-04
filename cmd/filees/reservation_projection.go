package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/internal/gui/projectionrefresh"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/passport"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationclient"
	"filees/pkg/talk"
)

// reservationProjectionCoordinator owns the desktop daemon's independent
// state lane. Transactional SVN operations never run through this object:
// every refresh opens the reservation-v1 SSH command on its own connection,
// while projectionrefresh guarantees at most one such request sequence per
// server and allows different servers to progress independently.
type reservationProjectionCoordinator struct {
	ctx       context.Context
	ipc       *ipcserver.Server
	scheduler *projectionrefresh.Scheduler
	newClient func(clientprofile.Profile) (reservationFetcher, error)
	// onServerViewProduced carries what the server said about publishing this
	// client's view. It rides the reservation fetch rather than a request of
	// its own: the query attaches to work already happening, which is what
	// keeps the session count where the server hardening expects it.
	onServerViewProduced func(serverID string, generation int64, producedAt time.Time)
	// onDetached carries the one fact no local measurement can produce: the
	// server was reached and refused this client. It rides the same fetch,
	// like onServerViewProduced, rather than asking a question of its own.
	onDetached func(serverID string, detached bool)

	mu       sync.RWMutex
	profiles map[string]clientprofile.Profile
	views    map[string]clientview.View
	results  map[reposupervisor.Key]cachedReservationResult
	// detached remembers which servers have told us this client is no longer
	// one of theirs, so the fact is stated once instead of every cycle.
	detached map[string]bool
	// paused is the transport consequence of detached. A revoked credential is
	// terminal, not an offline server: periodic ticks remain local and must not
	// open another SSH session until activation supplies a new profile.
	paused   map[string]bool
	overlays map[reposupervisor.Key]reservationLocalOverlay
	started  map[string]bool
}

type reservationFetcher interface {
	Fetch(context.Context, string) (reservationv1.Result, error)
}

type cachedReservationResult struct {
	result reservationv1.Result
	// offline means the server could not be reached. detached means it was
	// reached and refused us: this client is no longer one of its own. They
	// call for opposite responses - wait, versus activate again - so they are
	// two fields and never one.
	offline  bool
	detached bool
	present  bool
}

type reservationLocalOverlay struct {
	svn       client.Client
	wc        string
	passports *passport.Manager
}

func newReservationProjectionCoordinator(ctx context.Context, ipc *ipcserver.Server) *reservationProjectionCoordinator {
	coordinator := &reservationProjectionCoordinator{
		ctx: ctx, ipc: ipc,
		profiles: make(map[string]clientprofile.Profile),
		views:    make(map[string]clientview.View),
		results:  make(map[reposupervisor.Key]cachedReservationResult),
		detached: make(map[string]bool),
		paused:   make(map[string]bool),
		overlays: make(map[reposupervisor.Key]reservationLocalOverlay),
		started:  make(map[string]bool),
	}
	coordinator.newClient = func(profile clientprofile.Profile) (reservationFetcher, error) {
		timeout := profile.SVNTimeout()
		if timeout <= 0 || timeout > 90*time.Second {
			timeout = 90 * time.Second
		}
		return reservationclient.New(reservationclient.Config{
			Address: profile.Address, Port: profile.SSHPort,
			IdentityFile: profile.IdentityFile, KnownHosts: profile.KnownHosts,
			Timeout: timeout,
		})
	}
	coordinator.scheduler = projectionrefresh.New(ctx, coordinator.refresh)
	return coordinator
}

func (coordinator *reservationProjectionCoordinator) Close() {
	if coordinator != nil && coordinator.scheduler != nil {
		coordinator.scheduler.Close()
	}
}

// UpdateProfile installs transport parameters and starts one periodic trigger
// loop. The current view (if any) schedules the immediate refresh; keeping that
// ordering prevents a fast proof refusal from racing activation's restoration
// of the local pipelines.
func (coordinator *reservationProjectionCoordinator) UpdateProfile(profile clientprofile.Profile) {
	if coordinator == nil || profile.ServerID == "" {
		return
	}
	coordinator.mu.Lock()
	coordinator.profiles[profile.ServerID] = profile
	start := !coordinator.started[profile.ServerID]
	if start {
		coordinator.started[profile.ServerID] = true
	}
	coordinator.mu.Unlock()
	if start {
		go coordinator.periodic(profile.ServerID)
	}
}

// Profile returns the transport parameters known for serverID.
//
// The names live here because this is where they were last seen. A caller
// reacting to a detachment cannot go to the profile store for them: for a
// self-detachment the credentials have already been removed, and for a revoked
// client the server has stopped answering the question.
func (coordinator *reservationProjectionCoordinator) Profile(serverID string) (clientprofile.Profile, bool) {
	if coordinator == nil {
		return clientprofile.Profile{}, false
	}
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	profile, ok := coordinator.profiles[serverID]
	return profile, ok
}

func (coordinator *reservationProjectionCoordinator) periodic(serverID string) {
	for {
		coordinator.mu.RLock()
		interval := coordinator.profiles[serverID].PollInterval
		coordinator.mu.RUnlock()
		if interval <= 0 {
			interval = 30 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-coordinator.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			coordinator.scheduler.Schedule(serverID)
		}
	}
}

// Resume permits transport again after an explicit activation event. It does
// not clear the projected detached state: only the first successful exchange
// is evidence that the replacement credential is live.
func (coordinator *reservationProjectionCoordinator) Resume(serverID string) bool {
	if coordinator == nil || serverID == "" {
		return false
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.paused[serverID] {
		return false
	}
	delete(coordinator.paused, serverID)
	return true
}

func (coordinator *reservationProjectionCoordinator) Paused(serverID string) bool {
	if coordinator == nil || serverID == "" {
		return false
	}
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	return coordinator.paused[serverID]
}

// UpdateView publishes the current set of server-owned repositories into the
// state lane and wires their RepoState presenters, including remote folders
// without a local working copy.
func (coordinator *reservationProjectionCoordinator) UpdateView(serverID string, view clientview.View) {
	if coordinator == nil || serverID == "" {
		return
	}
	coordinator.mu.Lock()
	coordinator.views[serverID] = view
	projected := make(map[string]struct{}, len(view.Repositories))
	for _, repo := range view.Repositories {
		projected[repo.RepoID] = struct{}{}
	}
	for key := range coordinator.results {
		if key.ServerID != serverID {
			continue
		}
		if _, exists := projected[key.RepoID]; !exists {
			delete(coordinator.results, key)
		}
	}
	coordinator.mu.Unlock()
	coordinator.wireView(serverID, view)
	coordinator.scheduler.Schedule(serverID)
}

func (coordinator *reservationProjectionCoordinator) wireView(serverID string, view clientview.View) {
	if coordinator.ipc == nil {
		return
	}
	for _, projected := range view.Repositories {
		state := coordinator.ipc.RepoState(serverID, projected.RepoID)
		if state == nil {
			continue
		}
		key := reposupervisor.Key{ServerID: serverID, RepoID: projected.RepoID}
		state.SetReservationListFunc(func(ctx context.Context) (ipcserver.ReservationSnapshot, error) {
			return coordinator.snapshot(ctx, key)
		})
	}
}

func (coordinator *reservationProjectionCoordinator) AttachLocal(key reposupervisor.Key, svn client.Client, wc string, manager *passport.Manager) {
	if coordinator == nil || svn == nil || wc == "" {
		return
	}
	coordinator.mu.Lock()
	coordinator.overlays[key] = reservationLocalOverlay{svn: svn, wc: wc, passports: manager}
	coordinator.mu.Unlock()
}

func (coordinator *reservationProjectionCoordinator) DetachLocal(key reposupervisor.Key) {
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	delete(coordinator.overlays, key)
	coordinator.mu.Unlock()
}

func (coordinator *reservationProjectionCoordinator) Schedule(serverID string) {
	if coordinator == nil {
		return
	}
	coordinator.mu.RLock()
	paused := coordinator.paused[serverID]
	coordinator.mu.RUnlock()
	if !paused {
		coordinator.scheduler.Schedule(serverID)
	}
}

func (coordinator *reservationProjectionCoordinator) refresh(ctx context.Context, serverID string) {
	coordinator.mu.RLock()
	profile, hasProfile := coordinator.profiles[serverID]
	view, hasView := coordinator.views[serverID]
	paused := coordinator.paused[serverID]
	coordinator.mu.RUnlock()
	if !hasProfile || !hasView || paused {
		return
	}
	fetcher, err := coordinator.newClient(profile)
	if err != nil {
		coordinator.markServerOffline(serverID, view, err)
		return
	}
	repoIDs := make([]string, 0, len(view.Repositories))
	for _, repo := range view.Repositories {
		if repo.State == "active" {
			repoIDs = append(repoIDs, repo.RepoID)
		}
	}
	sort.Strings(repoIDs)
	skipped := len(view.Repositories) - len(repoIDs)
	fetched, failed := 0, 0
	var firstErr error
	var firstErrRepo string
	for _, repoID := range repoIDs {
		result, fetchErr := fetcher.Fetch(ctx, repoID)
		if isDetachedClient(fetchErr) {
			failed++
			firstErr, firstErrRepo = fetchErr, repoID
			break
		}
		key := reposupervisor.Key{ServerID: serverID, RepoID: repoID}
		coordinator.mu.Lock()
		if fetchErr != nil {
			cached := coordinator.results[key]
			cached.offline = true
			coordinator.results[key] = cached
		} else {
			// Keep the terminal presentation coherent throughout a validation
			// pass. One successful repo is not enough to reattach a server whose
			// remaining repos may still reject the same replacement proof.
			coordinator.results[key] = cachedReservationResult{result: result, present: true, detached: coordinator.detached[serverID]}
		}
		coordinator.mu.Unlock()
		// Every repository on one server shares that server's client view, so
		// the first answer settles it for the whole refresh.
		if fetchErr == nil && coordinator.onServerViewProduced != nil && result.ViewGeneratedAt != nil {
			coordinator.onServerViewProduced(serverID, result.ViewGeneration, *result.ViewGeneratedAt)
		}
		if fetchErr != nil {
			failed++
			if firstErr == nil {
				firstErr, firstErrRepo = fetchErr, repoID
			}
		} else {
			fetched++
		}
		if ctx.Err() != nil {
			return
		}
	}
	// One line per cycle, not one per repository. Two problems motivated
	// this. A successful cycle used to leave no trace at all, so "the lane
	// works" and "the lane is wedged" looked identical from the log and had
	// to be told apart by reading code. And a server without the worker
	// emitted one warning per repository per cycle, which buried everything
	// else: twelve repositories produced a warning every few seconds.
	lg := talk.With("reservation-projection:" + serverID)
	switch {
	case failed == 0 && fetched > 0:
		if coordinator.onDetached != nil {
			coordinator.onDetached(serverID, false)
		}
		coordinator.forgetDetached(serverID)
		lg.Infof("state refreshed: %d ok, %d not active", fetched, skipped)
	case failed > 0 && isDetachedClient(firstErr):
		coordinator.pauseDetached(serverID, view, firstErr)
		return
	case failed > 0:
		lg.Warnf("state refresh: %d ok, %d failed, %d not active; first failure repo %s: %v",
			fetched, failed, skipped, firstErrRepo, firstErr)
	}
	if coordinator.ipc != nil {
		coordinator.ipc.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
	}
}

// detectDetached is the view lane's entrance to the same terminal state. Both
// SSH commands authenticate with the same profile, so either authoritative
// refusal is enough to stop both periodic transports.
func (coordinator *reservationProjectionCoordinator) detectDetached(serverID string, cause error) {
	if coordinator == nil || serverID == "" || !isDetachedClient(cause) {
		return
	}
	coordinator.mu.RLock()
	view := coordinator.views[serverID]
	coordinator.mu.RUnlock()
	coordinator.pauseDetached(serverID, view, cause)
}

// pauseDetached turns one authoritative refusal into a stable local state.
// Every repository uses the same client credential, therefore the first
// refusal settles the whole server and further per-repository calls would only
// repeat a request which cannot succeed.
func (coordinator *reservationProjectionCoordinator) pauseDetached(serverID string, view clientview.View, cause error) {
	coordinator.mu.Lock()
	first := !coordinator.detached[serverID]
	coordinator.detached[serverID] = true
	coordinator.paused[serverID] = true
	for _, repo := range view.Repositories {
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		cached := coordinator.results[key]
		cached.detached = true
		cached.offline = false
		coordinator.results[key] = cached
	}
	coordinator.mu.Unlock()
	if coordinator.onDetached != nil {
		coordinator.onDetached(serverID, true)
	}
	if first {
		talk.With("reservation-projection:"+serverID).Warnf("ten klient został odłączony od serwera i nie ma tu już uprawnień; "+
			"odpytywanie zatrzymano do czasu ponownej aktywacji (%v)", cause)
	}
	if coordinator.ipc != nil {
		coordinator.ipc.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
	}
}

// isDetachedClient reports whether the server refused us because this client
// is no longer one of its own.
//
// The sentence comes from pkg/activation and is precise; what was missing is
// that it is terminal. It entered the same branch as a dropped connection,
// which carries a retry policy, so the daemon knocked once a minute forever
// while the interface called the server unavailable.
func isDetachedClient(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "proof does not match one live staged or active client")
}

// markDetached records the state and reports whether this is the first time,
// which is the only time it should be said.
func (coordinator *reservationProjectionCoordinator) markDetached(serverID string) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.detached == nil {
		coordinator.detached = map[string]bool{}
	}
	if coordinator.detached[serverID] {
		return false
	}
	coordinator.detached[serverID] = true
	return true
}

// forgetDetached clears the state so a re-activated client is announced again
// if it is ever detached a second time.
func (coordinator *reservationProjectionCoordinator) forgetDetached(serverID string) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	delete(coordinator.detached, serverID)
	for key, cached := range coordinator.results {
		if key.ServerID == serverID {
			cached.detached = false
			coordinator.results[key] = cached
		}
	}
}

func (coordinator *reservationProjectionCoordinator) markServerOffline(serverID string, view clientview.View, cause error) {
	coordinator.mu.Lock()
	for _, repo := range view.Repositories {
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		cached := coordinator.results[key]
		cached.offline = true
		coordinator.results[key] = cached
	}
	coordinator.mu.Unlock()
	talk.With("reservation-projection:"+serverID).Warnf("state lane unavailable: %v", cause)
	if coordinator.ipc != nil {
		coordinator.ipc.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
	}
}

func (coordinator *reservationProjectionCoordinator) snapshot(ctx context.Context, key reposupervisor.Key) (ipcserver.ReservationSnapshot, error) {
	coordinator.mu.RLock()
	cached := coordinator.results[key]
	overlay, attached := coordinator.overlays[key]
	coordinator.mu.RUnlock()
	if cached.detached && (!cached.present || cached.result.Unknown) {
		return ipcserver.ReservationSnapshot{Detached: true}, nil
	}
	if !cached.present || cached.result.Unknown {
		return ipcserver.ReservationSnapshot{Unknown: true}, nil
	}
	rows, err := overlayReservationRows(ctx, key.RepoID, cached.result.Reservations, overlay, attached)
	if err != nil {
		return ipcserver.ReservationSnapshot{}, err
	}
	return ipcserver.ReservationSnapshot{
		Reservations: rows, Stale: cached.result.Stale, Offline: cached.offline, Detached: cached.detached,
		AsOf: cached.result.AsOf, Generation: cached.result.Generation,
	}, nil
}

func overlayReservationRows(ctx context.Context, repoID string, source []reservationv1.Reservation, overlay reservationLocalOverlay, attached bool) ([]contract.Reservation, error) {
	localStatus := make(map[string]client.StatusEntry)
	localUnknown := false
	activePassports := make(map[string]passport.Passport)
	if attached {
		paths := make([]string, 0, len(source))
		for _, row := range source {
			paths = append(paths, filepath.Join(overlay.wc, filepath.FromSlash(row.Path)))
		}
		if len(paths) > 0 {
			entries, err := overlay.svn.Status(ctx, overlay.wc, paths)
			if err != nil {
				localUnknown = true
			} else {
				for _, entry := range entries {
					localStatus[filepath.ToSlash(filepath.Clean(entry.Path))] = entry
				}
			}
		}
		if overlay.passports != nil {
			for _, item := range overlay.passports.Snapshot() {
				if item.State == passport.StateActive {
					activePassports[filepath.Clean(item.Path)] = item
				}
			}
		}
	}
	rows := make([]contract.Reservation, 0, len(source))
	for _, remote := range source {
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(remote.Path)))
		absolute := ""
		activePassport := false
		canRelease := false
		if attached {
			absolute = filepath.Join(overlay.wc, filepath.FromSlash(path))
			item, exists := activePassports[filepath.Clean(absolute)]
			activePassport = exists && item.FencingToken == remote.Token
			_, passportLock := passport.ParseComment(remote.Comment)
			canRelease = overlay.passports == nil || activePassport || !passportLock
		}
		status := localStatus[path]
		localChanges := localUnknown || reservationStatusHasLocalChanges(status)
		rows = append(rows, contract.Reservation{
			RepoID: repoID, WorkingCopy: overlay.wc, Path: path, Token: remote.Token,
			OwnerID: remote.OwnerID, CreatedAt: remote.CreatedAt,
			CanRelease: canRelease, LocalChanges: localChanges,
			ActivePassport: activePassport,
		})
	}
	return rows, nil
}

func reservationStatusHasLocalChanges(entry client.StatusEntry) bool {
	return (entry.Item != "" && entry.Item != "normal" && entry.Item != "none") ||
		(entry.Props != "" && entry.Props != "normal" && entry.Props != "none")
}
