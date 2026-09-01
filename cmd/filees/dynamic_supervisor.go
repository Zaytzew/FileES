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

func runDynamicSupervisedRepositories(ctx context.Context, repos []config.Repo, activation config.ClientView, profiles []clientprofile.Profile, profileEvents <-chan clientprofile.Profile, timeoutEvents <-chan clientprofile.Profile, attachmentEvents <-chan provisionedAttachment, publicShareEvents <-chan string, ipc *ipcserver.Server, lifecycle *localrepo.Store, gate runtime.Gate, mutex runtime.RepoMutex, activityJournal *activity.Journal, projectRealmAlias func(serverID, realmID, projected string) string, shareLister publicShareLister, shareCache publicShareCacheSetter) error {
	reservationRefreshes := newReservationProjectionCoordinator(ctx, ipc)
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
	monitored := make(map[string]bool)
	currentViews := make(map[string]clientview.View)
	shareRefreshes := newPublicShareRefreshCoordinator(ctx, shareLister, shareCache, ipc)
	startMonitor := func(serverID, displayName, address, clientID, identityFile, knownHosts string, sshPort int, serviceURL string, sync clientview.SyncConfig, interval, timeout time.Duration) error {
		if timeout <= 0 {
			timeout = clientprofile.DefaultSessionTimeout
		}
		if monitored[serverID] {
			return nil
		}
		cached, exists, err := clientview.CachedOrNone(sync.CachePath)
		if err != nil {
			return fmt.Errorf("load cached projection: %w", err)
		}
		if exists {
			currentViews[serverID] = cached
			if err := reconcileProjectedView(ctx, supervisor, ipc, serverID, cached, runtimes, lifecycle); err != nil {
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
		ipc.RegisterActivation(contract.ActivationStatus{ServerID: serverID, DisplayName: displayName, ClientRole: clientRole, RealmID: realmID, RealmAlias: realmAlias, Address: address, ClientID: clientID, SSHPort: sshPort, CanCreateRepositories: canCreate, RepositoriesReady: ready, PendingRequiredRepos: pendingRequired, SessionTimeoutMin: int(timeout / time.Minute)})
		svn := client.New(client.Options{SvnPath: "svn", Timeout: timeout, LogScope: "svn:projection:" + serverID, SSHIdentityFile: identityFile, SSHKnownHosts: knownHosts, SSHPort: sshPort})
		var updater clientview.Updater = svn
		if serviceURL != "" {
			updater = serviceProjectionUpdater{client: svn, url: serviceURL}
		}
		views := clientview.Monitor(ctx, updater, clientview.MonitorConfig{Sync: sync, Interval: interval, OnError: func(err error) { talk.With("projection:"+serverID).Warnf("sync failed: %v", err) }})
		go func() {
			for view := range views {
				select {
				case updates <- projectionUpdate{serverID: serverID, displayName: displayName, address: address, clientID: clientID, sshPort: sshPort, view: view}:
				case <-ctx.Done():
					return
				}
			}
		}()
		return nil
	}
	startProfile := func(profile clientprofile.Profile) error {
		reservationRefreshes.UpdateProfile(profile)
		return startMonitor(profile.ServerID, profile.ServerID, profile.Address, profile.ClientID, profile.IdentityFile, profile.KnownHosts, profile.SSHPort, profile.ServiceURL, clientview.SyncConfig{WorkingCopy: profile.ServiceWC, RelativeViewPath: profile.RelativeViewPath, CachePath: profile.CachePath}, profile.PollInterval, profile.SVNTimeout())
	}
	for _, profile := range profiles {
		if err := startProfile(profile); err != nil {
			talk.With("projection:"+profile.ServerID).Errorf("restore profile: %v", err)
		}
	}
	if projection := activation.Projection; projection != nil && !monitored[activation.ServerID] {
		if err := startMonitor(activation.ServerID, activation.DisplayName, "", "", activation.IdentityFile, activation.KnownHosts, 0, "", clientview.SyncConfig{WorkingCopy: projection.WorkingCopy, RelativeViewPath: projection.RelativeViewPath, CachePath: projection.CachePath}, projection.Interval, 0); err != nil {
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
			if err := startProfile(profile); err != nil {
				talk.With("projection:"+profile.ServerID).Errorf("start activated profile: %v", err)
			}
		case profile := <-timeoutEvents:
			reservationRefreshes.UpdateProfile(profile)
			stampSessionTimeout(runtimes, profile.ServerID, profile.SVNTimeout())
			if view, ok := currentViews[profile.ServerID]; ok {
				if err := reconcileProjectedView(ctx, supervisor, ipc, profile.ServerID, view, runtimes, lifecycle); err != nil && ctx.Err() == nil {
					talk.With("projection:"+profile.ServerID).Errorf("apply session timeout: %v", err)
				}
			}
		case update := <-updates:
			currentViews[update.serverID] = update.view
			ready, pendingRequired := repositoryReadiness(update.serverID, update.view, runtimes)
			realmAlias := update.view.RealmAlias
			if projectRealmAlias != nil {
				realmAlias = projectRealmAlias(update.serverID, update.view.RealmID, realmAlias)
			}
			ipc.RegisterActivation(contract.ActivationStatus{ServerID: update.serverID, DisplayName: update.displayName, ClientRole: update.view.ClientRole, RealmID: update.view.RealmID, RealmAlias: realmAlias, Address: update.address, ClientID: update.clientID, SSHPort: update.sshPort, CanCreateRepositories: update.view.CanCreateRepositories(), RepositoriesReady: ready, PendingRequiredRepos: pendingRequired, SessionTimeoutMin: sessionTimeoutMinutes(update.serverID, runtimes)})
			if err := reconcileProjectedView(ctx, supervisor, ipc, update.serverID, update.view, runtimes, lifecycle); err != nil {
				if ctx.Err() == nil {
					talk.With("projection:"+update.serverID).Errorf("reconcile generation %d: %v", update.view.Generation, err)
				}
				continue
			}
			reservationRefreshes.UpdateView(update.serverID, update.view)
			shareRefreshes.Schedule(update.serverID, update.view)
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
