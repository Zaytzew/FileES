package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"filees/pkg/activity"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/detachment"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/reposupervisor"
	"filees/pkg/runtime"
	"filees/pkg/talk"
)

type projectionUpdate struct {
	serverID, displayName, address, clientID string
	sshPort                                  int
	view                                     clientview.View
}

func projectedServerDisplayName(beforeProjection string, view clientview.View) string {
	if view.ServerDisplayName != "" {
		return view.ServerDisplayName
	}
	return beforeProjection
}

// publicShareLister is the narrow slice of realmAliasService this file needs:
// one control-plane exchange per owned repo, unrelated to the GUI/IPC
// contract types it also happens to return.
type publicShareLister interface {
	ListPublicShares(ctx context.Context, serverID, repoID string) ([]contract.PublicShareSummary, error)
}

// publicShareCacheSetter is the write side of publicShareCache.
type publicShareCacheSetter interface {
	Set(serverID string, shares []contract.PublicShareSummary)
}

// publicShareRefreshCoordinator serialises aggregate refreshes per server.
// A mutation arriving while a refresh is already in flight is remembered and
// causes one more pass with the newest view; dropping that signal could leave
// the dashboard stale until an unrelated projection generation changes.
type publicShareRefreshCoordinator struct {
	ctx    context.Context
	lister publicShareLister
	cache  publicShareCacheSetter
	ipc    *ipcserver.Server

	mu      sync.Mutex
	running map[string]bool
	pending map[string]clientview.View
}

func newPublicShareRefreshCoordinator(ctx context.Context, lister publicShareLister, cache publicShareCacheSetter, ipc *ipcserver.Server) *publicShareRefreshCoordinator {
	return &publicShareRefreshCoordinator{
		ctx: ctx, lister: lister, cache: cache, ipc: ipc,
		running: make(map[string]bool), pending: make(map[string]clientview.View),
	}
}

func (coordinator *publicShareRefreshCoordinator) Schedule(serverID string, view clientview.View) {
	if coordinator == nil || coordinator.lister == nil || coordinator.cache == nil || serverID == "" || view.RealmID == "" {
		return
	}
	coordinator.mu.Lock()
	if coordinator.running[serverID] {
		coordinator.pending[serverID] = view
		coordinator.mu.Unlock()
		return
	}
	coordinator.running[serverID] = true
	coordinator.mu.Unlock()
	go coordinator.run(serverID, view)
}

func (coordinator *publicShareRefreshCoordinator) run(serverID string, view clientview.View) {
	for {
		refreshPublicShares(coordinator.ctx, coordinator.lister, coordinator.cache, coordinator.ipc, serverID, view)
		coordinator.mu.Lock()
		next, rerun := coordinator.pending[serverID]
		if rerun {
			delete(coordinator.pending, serverID)
			coordinator.mu.Unlock()
			view = next
			continue
		}
		delete(coordinator.running, serverID)
		coordinator.mu.Unlock()
		return
	}
}

// refreshPublicShares aggregates every publicly-shared channel this realm
// owns on serverID and republishes the result into cache, then notifies any
// listening GUI. It runs off the same cadence as the server's projection
// sync (clientview.Monitor's configured interval) — piggybacked onto an
// already-throttled, already-scheduled poll instead of a new ticker, so it
// never adds SSH control-plane load beyond what that interval already
// budgets for. Called from a bounded-lifetime goroutine (see updates
// handling below), never from the supervisor's own select loop, since each
// owned repo costs one blocking SSH exchange.
func refreshPublicShares(ctx context.Context, lister publicShareLister, cache publicShareCacheSetter, ipc *ipcserver.Server, serverID string, view clientview.View) {
	if lister == nil || cache == nil || view.RealmID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	shares := make([]contract.PublicShareSummary, 0, len(view.Repositories))
	for _, repo := range view.Repositories {
		if repo.OwnerRealmID != view.RealmID || repo.State != "active" {
			continue
		}
		listed, err := lister.ListPublicShares(ctx, serverID, repo.RepoID)
		if err != nil {
			talk.With("public-shares:"+serverID).Warnf("aggregate listing failed for repo %s: %v", repo.RepoID, err)
			continue
		}
		for _, share := range listed {
			share.ServerID = serverID
			share.RepoDisplayName = repo.DisplayName
			shares = append(shares, share)
		}
	}
	cache.Set(serverID, shares)
	if ipc != nil {
		ipc.Emit(contract.NewEvent("", 0, contract.EvPublicSharesChanged, "", nil))
	}
}

type serviceProjectionUpdater struct {
	client client.Client
	url    string
}

func (updater serviceProjectionUpdater) Update(ctx context.Context, workingCopy string) (string, error) {
	if _, err := os.Stat(filepath.Join(workingCopy, ".svn")); err == nil {
		return updater.client.Update(ctx, workingCopy)
	}
	if err := os.MkdirAll(filepath.Dir(workingCopy), 0o700); err != nil {
		return "", err
	}
	return updater.client.Checkout(ctx, updater.url, workingCopy)
}

// Cleanup satisfies clientview.Cleaner so a service working copy left locked by
// an interrupted operation can be released and the sync retried.
//
// Without it the wrapper exposed only Update, the type assertion in
// clientview.Sync failed silently, and the release added in r786 never ran once
// in production - the lock simply reappeared every cycle and the interface went
// on reporting the server as unreachable. An optional interface is only
// optional for implementations that genuinely cannot satisfy it; this one wraps
// a client that has always had Cleanup.
func (updater serviceProjectionUpdater) Cleanup(ctx context.Context, workingCopy string) (string, error) {
	return updater.client.Cleanup(ctx, workingCopy)
}

func runDynamicSupervisedRepositories(ctx context.Context, repos []config.Repo, activation config.ClientView, profiles []clientprofile.Profile, profileEvents <-chan clientprofile.Profile, timeoutEvents <-chan clientprofile.Profile, attachmentEvents <-chan provisionedAttachment, publicShareEvents <-chan string, ipc *ipcserver.Server, lifecycle *localrepo.Store, detachments *detachment.Store, gate runtime.Gate, mutex runtime.RepoMutex, activityJournal *activity.Journal, projectRealmAlias func(serverID, realmID, projected string) string, shareLister publicShareLister, shareCache publicShareCacheSetter) error {
	// One recorder for every server this supervisor watches: the view lane is
	// per server and so is its age.
	freshness := newViewFreshness(nil)
	// One publisher per server, registered when its monitor starts, so any
	// source of freshness can push the snapshot without rebuilding the record.
	freshnessPublishers := map[string]func(){}
	var freshnessMu sync.Mutex
	monitorCancels := map[string]context.CancelFunc{}
	var monitorMu sync.Mutex
	stopMonitor := func(serverID string) {
		monitorMu.Lock()
		cancel := monitorCancels[serverID]
		monitorMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	detachedEvents := make(chan string, 2*(len(profiles)+1))
	reservationRefreshes := newReservationProjectionCoordinator(ctx, ipc)
	// The state lane already talks to the server on a rhythm driven by real
	// work, so what the server says about producing our view arrives with it
	// rather than on a schedule of its own.
	reservationRefreshes.onDetached = func(serverID string, detached bool) {
		freshness.Detached(serverID, detached)
		if detached {
			stopMonitor(serverID)
			select {
			case detachedEvents <- serverID:
			case <-ctx.Done():
			}
		}
		freshnessMu.Lock()
		publish := freshnessPublishers[serverID]
		freshnessMu.Unlock()
		if publish != nil {
			publish()
		}
		// The durable half. freshness.Detached is a flag describing the server
		// right now and dies with the process; this is the moment, and it has
		// to outlive a daemon restart or a forty-eight hour lifetime measured
		// from it is not a lifetime at all.
		//
		// Both edges are handled here rather than in the coordinator: this is
		// the only place that can see the local working copies as well, and
		// where those files are is what the reader will actually want.
		if detachments == nil {
			return
		}
		if !detached {
			// Only a genuinely successful refresh reaches this edge, which is
			// the one piece of evidence that the client is one of the
			// server's own again. A transient failure never gets here, so it
			// cannot quietly erase a detachment that still stands.
			_, _ = detachments.Reattached(serverID, time.Now())
			return
		}
		profile, _ := reservationRefreshes.Profile(serverID)
		_, _ = detachments.RecordFirstNoticed(detachment.Record{
			ServerID: serverID, DisplayName: profile.DisplayName, Address: profile.Address,
			Cause: detachment.CauseRevoked, At: time.Now(),
			WorkingCopies: workingCopiesOf(lifecycle, serverID),
		})
	}
	reservationRefreshes.onServerViewProduced = func(serverID string, generation int64, producedAt time.Time) {
		freshness.Produced(serverID, generation, producedAt)
		freshnessMu.Lock()
		publish := freshnessPublishers[serverID]
		freshnessMu.Unlock()
		if publish != nil {
			publish()
		}
	}
	defer reservationRefreshes.Close()
	runtimes := make(map[reposupervisor.Key]repoRuntime, len(repos))
	byServer := make(map[string][]reposupervisor.Desired)
	timeouts := make(map[string]time.Duration, len(profiles))
	for _, profile := range profiles {
		timeouts[profile.ServerID] = profile.SVNTimeout()
	}
	for _, repo := range repos {
		key := reposupervisor.Key{ServerID: repo.ServerID, RepoID: repo.ID}
		if repo.SessionTimeout <= 0 {
			repo.SessionTimeout = timeouts[repo.ServerID]
		}
		state := ipc.RegisterRepoAccess(repo.ID, repo.RepoURL, repo.LocalPath, repo.ServerID, repo.Access)
		runtimes[key] = repoRuntime{config: repo, state: state}
		byServer[repo.ServerID] = append(byServer[repo.ServerID], reposupervisor.Desired{Key: key, Access: repo.Access, State: "active", URL: repo.RepoURL, DisplayName: repo.ID, SessionTimeout: repo.SessionTimeout})
	}
	deps := readWriteDependencies{gate: gate, mutex: mutex, ipc: ipc, activity: activityJournal, reservations: reservationRefreshes}
	starter := &daemonRepoStarter{daemonCtx: ctx, repos: runtimes, newSVN: func(repo config.Repo) client.Client {
		timeout := repo.SessionTimeout
		if timeout <= 0 {
			timeout = clientprofile.DefaultSessionTimeout
		}
		return client.New(client.Options{SvnPath: "svn", Timeout: timeout, LogScope: "svn:" + repo.ID, SSHIdentityFile: repo.SSHIdentityFile, SSHKnownHosts: repo.SSHKnownHosts, SSHHostName: repo.SSHHostName, SSHPort: repo.SSHPort})
	}, reservations: reservationRefreshes}
	starter.startReadWrite = func(lifecycle context.Context, runtimeRepo repoRuntime, svn client.Client, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
		return startReadWrite(lifecycle, runtimeRepo, svn, desired, deps)
	}
	supervisor, err := reposupervisor.New(starter, nil)
	if err != nil {
		return err
	}
	updates := make(chan projectionUpdate, 16)
	// Monitor's output channel carries only changed server generations. Local
	// durable state can change while that generation stays still, so successful
	// unchanged polls need a lightweight local-overlay projection as well.
	syncs := make(chan projectionUpdate, 16)
	monitored := make(map[string]bool)
	currentViews := make(map[string]clientview.View)
	shareRefreshes := newPublicShareRefreshCoordinator(ctx, shareLister, shareCache, ipc)
	startMonitor := func(serverID, displayName, address, clientID, identityFile, knownHosts string, sshPort int, serviceURL string, syncConfig clientview.SyncConfig, interval, timeout time.Duration, resumeCached bool) error {
		if timeout <= 0 {
			timeout = clientprofile.DefaultSessionTimeout
		}
		if monitored[serverID] {
			return nil
		}
		monitorCtx, cancelMonitor := context.WithCancel(ctx)
		monitorMu.Lock()
		monitorCancels[serverID] = cancelMonitor
		monitorMu.Unlock()
		cached, exists, err := clientview.CachedOrNone(syncConfig.CachePath)
		if err != nil {
			cancelMonitor()
			return fmt.Errorf("load cached projection: %w", err)
		}
		currentDisplayName := displayName
		if exists {
			currentDisplayName = projectedServerDisplayName(currentDisplayName, cached)
		}
		var displayNameMu sync.RWMutex
		setDisplayName := func(view clientview.View) {
			displayNameMu.Lock()
			currentDisplayName = projectedServerDisplayName(currentDisplayName, view)
			displayNameMu.Unlock()
		}
		displayNameNow := func() string {
			displayNameMu.RLock()
			defer displayNameMu.RUnlock()
			return currentDisplayName
		}
		if exists {
			currentViews[serverID] = cached
			if resumeCached {
				applyRealmOwnership(serverID, cached, runtimes)
				desired := attachedProjection(serverID, cached, runtimes)
				if err := supervisor.ApplyLocalAttachment(ctx, serverID, cached.Generation, desired, func(item reposupervisor.Desired) {
					if runtime, ok := runtimes[item.Key]; ok {
						runtime.state.SetProjection(item.URL, item.Access)
					}
				}); err != nil {
					cancelMonitor()
					return fmt.Errorf("resume cached projection: %w", err)
				}
				syncProjectionKnowledge(ipc, serverID, cached, runtimes, lifecycle)
			} else if err := reconcileProjectedView(ctx, supervisor, ipc, serverID, cached, runtimes, lifecycle); err != nil {
				cancelMonitor()
				return fmt.Errorf("apply cached projection: %w", err)
			}
			reservationRefreshes.UpdateView(serverID, cached)
			// Monitor emits only a newer generation. Seed the aggregate from the
			// already-validated cache as well, otherwise a quiet server leaves E1
			// empty for the daemon's entire lifetime.
			shareRefreshes.Schedule(serverID, cached)
		}
		monitored[serverID] = true
		clientRole := "normal"
		canCreate := false
		if exists {
			clientRole = cached.ClientRole
			canCreate = cached.CanCreateRepositories()
		}
		ready, pendingRequired := false, 0
		if exists {
			ready, pendingRequired = repositoryReadiness(serverID, cached, runtimes)
		}
		realmID := ""
		if exists {
			realmID = cached.RealmID
		}
		realmAlias := ""
		if exists {
			realmAlias = cached.RealmAlias
		}
		if projectRealmAlias != nil {
			realmAlias = projectRealmAlias(serverID, realmID, realmAlias)
		}
		ipc.RegisterActivation(freshness.Apply(contract.ActivationStatus{ServerID: serverID, DisplayName: displayNameNow(), ClientRole: clientRole, RealmID: realmID, RealmAlias: realmAlias, Address: address, ClientID: clientID, SSHPort: sshPort, CanCreateRepositories: canCreate, RepositoriesReady: ready, PendingRequiredRepos: pendingRequired, SessionTimeoutMin: int(timeout / time.Minute)}))
		svn := client.New(client.Options{SvnPath: "svn", Timeout: timeout, LogScope: "svn:projection:" + serverID, SSHIdentityFile: identityFile, SSHKnownHosts: knownHosts, SSHPort: sshPort})
		var updater clientview.Updater = svn
		if serviceURL != "" {
			updater = serviceProjectionUpdater{client: svn, url: serviceURL}
		}
		// Recording a sync outcome is useless unless it reaches the snapshot,
		// and only this closure does that. Both handlers below must call it:
		// an unpublished failure is exactly the log-line-and-nothing-else that
		// left a ten-day-old projection looking current.
		publishFreshness := func() {
			ipc.RegisterActivation(freshness.Apply(contract.ActivationStatus{ServerID: serverID, DisplayName: displayNameNow(), ClientRole: clientRole, RealmID: realmID, RealmAlias: realmAlias, Address: address, ClientID: clientID, SSHPort: sshPort, CanCreateRepositories: canCreate, RepositoriesReady: ready, PendingRequiredRepos: pendingRequired, SessionTimeoutMin: int(timeout / time.Minute)}))
		}
		freshnessMu.Lock()
		freshnessPublishers[serverID] = publishFreshness
		freshnessMu.Unlock()
		views := clientview.Monitor(monitorCtx, updater, clientview.MonitorConfig{Sync: syncConfig, Interval: interval, OnError: func(err error) {
			if isDetachedClient(err) {
				// The view and reservation commands use the same client proof.
				// One authoritative refusal ends both retry loops; activation is
				// the only event that may resume them.
				reservationRefreshes.detectDetached(serverID, err)
				return
			}
			freshness.Failed(serverID, err)
			publishFreshness()
			talk.With("projection:"+serverID).Warnf("sync failed: %v", err)
		}, OnSync: func(view clientview.View) {
			// Every successful sync, not only one that brought a new
			// generation: a quiet server that changes nothing must not look
			// like a server that has stopped answering.
			setDisplayName(view)
			freshness.Synced(serverID, view)
			publishFreshness()
			select {
			case syncs <- projectionUpdate{serverID: serverID, view: view}:
			case <-monitorCtx.Done():
			}
		}})
		go func() {
			for view := range views {
				select {
				case updates <- projectionUpdate{serverID: serverID, displayName: displayNameNow(), address: address, clientID: clientID, sshPort: sshPort, view: view}:
				case <-ctx.Done():
					return
				}
			}
		}()
		return nil
	}
	startProfile := func(profile clientprofile.Profile, activationEvent bool) error {
		wasMonitored := monitored[profile.ServerID]
		if activationEvent {
			reservationRefreshes.Resume(profile.ServerID)
		}
		resumeCached := activationEvent && wasMonitored
		if resumeCached {
			// Activation may replace the key and transport paths even when the
			// previous profile had not yet observed its own revocation. Never
			// leave a live monitor bound to the old credential.
			stopMonitor(profile.ServerID)
			monitored[profile.ServerID] = false
		}
		reservationRefreshes.UpdateProfile(profile)
		return startMonitor(profile.ServerID, profile.DisplayName, profile.Address, profile.ClientID, profile.IdentityFile, profile.KnownHosts, profile.SSHPort, profile.ServiceURL, clientview.SyncConfig{WorkingCopy: profile.ServiceWC, RelativeViewPath: profile.RelativeViewPath, CachePath: profile.CachePath}, profile.PollInterval, profile.SVNTimeout(), resumeCached)
	}
	for _, profile := range profiles {
		if err := startProfile(profile, false); err != nil {
			talk.With("projection:"+profile.ServerID).Errorf("restore profile: %v", err)
		}
	}
	if projection := activation.Projection; projection != nil && !monitored[activation.ServerID] {
		if err := startMonitor(activation.ServerID, activation.DisplayName, "", "", activation.IdentityFile, activation.KnownHosts, 0, "", clientview.SyncConfig{WorkingCopy: projection.WorkingCopy, RelativeViewPath: projection.RelativeViewPath, CachePath: projection.CachePath}, projection.Interval, 0, false); err != nil {
			talk.With("projection:"+activation.ServerID).Errorf("start configured monitor: %v", err)
		}
	}
	servers := make([]string, 0, len(byServer))
	for serverID := range byServer {
		servers = append(servers, serverID)
	}
	sort.Strings(servers)
	for _, serverID := range servers {
		if monitored[serverID] {
			continue
		}
		if err := supervisor.Apply(ctx, serverID, 1, byServer[serverID]); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			_ = supervisor.Stop(stopCtx)
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			return supervisor.Stop(stopCtx)
		case profile := <-profileEvents:
			if err := startProfile(profile, true); err != nil {
				talk.With("projection:"+profile.ServerID).Errorf("start activated profile: %v", err)
			}
		case serverID := <-detachedEvents:
			// Activation and suspension are serialized in this loop. If a new
			// profile won the race, a late event from the old proof must not stop
			// the replacement pipelines.
			if !reservationRefreshes.Paused(serverID) {
				continue
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			if err := supervisor.SuspendServer(stopCtx, serverID); err != nil {
				talk.With("projection:"+serverID).Warnf("suspend detached repositories: %v", err)
			}
			cancel()
		case profile := <-timeoutEvents:
			reservationRefreshes.UpdateProfile(profile)
			stampSessionTimeout(runtimes, profile.ServerID, profile.SVNTimeout())
			if view, ok := currentViews[profile.ServerID]; ok {
				if err := reconcileProjectedView(ctx, supervisor, ipc, profile.ServerID, view, runtimes, lifecycle); err != nil && ctx.Err() == nil {
					talk.With("projection:"+profile.ServerID).Errorf("apply session timeout: %v", err)
				}
				reservationRefreshes.Schedule(profile.ServerID)
			}
		case update := <-updates:
			currentViews[update.serverID] = update.view
			freshness.Synced(update.serverID, update.view)
			ready, pendingRequired := repositoryReadiness(update.serverID, update.view, runtimes)
			realmAlias := update.view.RealmAlias
			if projectRealmAlias != nil {
				realmAlias = projectRealmAlias(update.serverID, update.view.RealmID, realmAlias)
			}
			ipc.RegisterActivation(freshness.Apply(contract.ActivationStatus{ServerID: update.serverID, DisplayName: update.displayName, ClientRole: update.view.ClientRole, RealmID: update.view.RealmID, RealmAlias: realmAlias, Address: update.address, ClientID: update.clientID, SSHPort: update.sshPort, CanCreateRepositories: update.view.CanCreateRepositories(), RepositoriesReady: ready, PendingRequiredRepos: pendingRequired, SessionTimeoutMin: sessionTimeoutMinutes(update.serverID, runtimes)}))
			if err := reconcileProjectedView(ctx, supervisor, ipc, update.serverID, update.view, runtimes, lifecycle); err != nil {
				if ctx.Err() == nil {
					talk.With("projection:"+update.serverID).Errorf("reconcile generation %d: %v", update.view.Generation, err)
				}
				continue
			}
			reservationRefreshes.UpdateView(update.serverID, update.view)
			shareRefreshes.Schedule(update.serverID, update.view)
		case synced := <-syncs:
			// A newer generation is reconciled by the updates lane. For the
			// generation already held here, recompute only daemon-owned overlays
			// without inventing a new server generation.
			syncProjectionOnSuccessfulPoll(ipc, synced, currentViews, runtimes, lifecycle)
		case serverID := <-publicShareEvents:
			if view, ok := currentViews[serverID]; ok {
				shareRefreshes.Schedule(serverID, view)
			}
		case attachment := <-attachmentEvents:
			repo := attachment.Repo
			key := reposupervisor.Key{ServerID: repo.ServerID, RepoID: repo.ID}
			if attachment.Quiesce {
				old, exists := runtimes[key]
				view, hasView := currentViews[repo.ServerID]
				if attachment.Detach {
					if exists {
						delete(runtimes, key)
					}
					err := supervisor.DetachLocal(ctx, key)
					if err != nil {
						if exists {
							runtimes[key] = old
						}
					} else if hasView {
						syncProjectionKnowledge(ipc, repo.ServerID, view, runtimes, lifecycle)
						reservationRefreshes.UpdateView(repo.ServerID, view)
					} else {
						ipc.MarkRepoDetached(repo.ServerID, repo.ID)
					}
					attachment.Result <- err
					continue
				}
				if !hasView {
					attachment.Result <- fmt.Errorf("attached runtime or authoritative projection is unavailable")
					continue
				}
				if !exists {
					attachment.Result <- fmt.Errorf("attached runtime or authoritative projection is unavailable")
					continue
				}
				delete(runtimes, key)
				desired := attachedProjection(repo.ServerID, view, runtimes)
				err := supervisor.ApplyLocalAttachment(ctx, repo.ServerID, view.Generation, desired, nil)
				if err != nil {
					runtimes[key] = old
				}
				attachment.Result <- err
				continue
			}
			if _, exists := runtimes[key]; exists {
				continue
			}
			state := ipc.RegisterRepoAccess(repo.ID, repo.RepoURL, repo.LocalPath, repo.ServerID, repo.Access)
			runtimes[key] = repoRuntime{config: repo, state: state}
			view, exists := currentViews[repo.ServerID]
			if !exists {
				continue
			}
			applyRealmOwnership(repo.ServerID, view, runtimes)
			desired := attachedProjection(repo.ServerID, view, runtimes)
			if err := supervisor.ApplyLocalAttachment(ctx, repo.ServerID, view.Generation, desired, func(item reposupervisor.Desired) {
				if runtime, ok := runtimes[item.Key]; ok {
					runtime.state.SetProjection(item.URL, item.Access)
				}
			}); err != nil && ctx.Err() == nil {
				talk.With("projection:"+repo.ServerID).Errorf("attach repository %s: %v", repo.ID, err)
			}
			syncProjectionKnowledge(ipc, repo.ServerID, view, runtimes, lifecycle)
			reservationRefreshes.UpdateView(repo.ServerID, view)
		}
	}
}

func syncProjectionOnSuccessfulPoll(ipc *ipcserver.Server, synced projectionUpdate, currentViews map[string]clientview.View, runtimes map[reposupervisor.Key]repoRuntime, lifecycle *localrepo.Store) bool {
	current, ok := currentViews[synced.serverID]
	if !ok || current.Generation != synced.view.Generation {
		return false
	}
	syncProjectionKnowledge(ipc, synced.serverID, synced.view, runtimes, lifecycle)
	return true
}

func stampSessionTimeout(runtimes map[reposupervisor.Key]repoRuntime, serverID string, timeout time.Duration) {
	for key, runtime := range runtimes {
		if key.ServerID != serverID {
			continue
		}
		runtime.config.SessionTimeout = timeout
		runtimes[key] = runtime
	}
}

func sessionTimeoutMinutes(serverID string, runtimes map[reposupervisor.Key]repoRuntime) int {
	for key, runtime := range runtimes {
		if key.ServerID != serverID {
			continue
		}
		timeout := runtime.config.SessionTimeout
		if timeout <= 0 {
			timeout = clientprofile.DefaultSessionTimeout
		}
		return int(timeout / time.Minute)
	}
	return int(clientprofile.DefaultSessionTimeout / time.Minute)
}
