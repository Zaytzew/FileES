package main

import (
	"context"
	"path/filepath"
	"sort"
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

	mu       sync.RWMutex
	profiles map[string]clientprofile.Profile
	views    map[string]clientview.View
	results  map[reposupervisor.Key]cachedReservationResult
	overlays map[reposupervisor.Key]reservationLocalOverlay
	started  map[string]bool
}

type reservationFetcher interface {
	Fetch(context.Context, string) (reservationv1.Result, error)
}

type cachedReservationResult struct {
	result  reservationv1.Result
	offline bool
	present bool
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
// loop. The loop only schedules work; projectionrefresh owns coalescing.
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
	coordinator.scheduler.Schedule(profile.ServerID)
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
	if coordinator != nil {
		coordinator.scheduler.Schedule(serverID)
	}
}

func (coordinator *reservationProjectionCoordinator) refresh(ctx context.Context, serverID string) {
	coordinator.mu.RLock()
	profile, hasProfile := coordinator.profiles[serverID]
	view, hasView := coordinator.views[serverID]
	coordinator.mu.RUnlock()
	if !hasProfile || !hasView {
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
	for _, repoID := range repoIDs {
		result, fetchErr := fetcher.Fetch(ctx, repoID)
		key := reposupervisor.Key{ServerID: serverID, RepoID: repoID}
		coordinator.mu.Lock()
		if fetchErr != nil {
			cached := coordinator.results[key]
			cached.offline = true
			coordinator.results[key] = cached
		} else {
			coordinator.results[key] = cachedReservationResult{result: result, present: true}
		}
		coordinator.mu.Unlock()
		if fetchErr != nil {
			talk.With("reservation-projection:"+serverID).Warnf("repo %s state refresh failed: %v", repoID, fetchErr)
		}
		if ctx.Err() != nil {
			return
		}
	}
	if coordinator.ipc != nil {
		coordinator.ipc.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
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
	if !cached.present || cached.result.Unknown {
		return ipcserver.ReservationSnapshot{Unknown: true}, nil
	}
	rows, err := overlayReservationRows(ctx, key.RepoID, cached.result.Reservations, overlay, attached)
	if err != nil {
		return ipcserver.ReservationSnapshot{}, err
	}
	return ipcserver.ReservationSnapshot{
		Reservations: rows, Stale: cached.result.Stale, Offline: cached.offline,
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
