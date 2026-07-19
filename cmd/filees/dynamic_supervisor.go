package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	"filees/pkg/runtime"
	"filees/pkg/talk"
)

type projectionUpdate struct {
	serverID, displayName string
	view                  clientview.View
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

func runDynamicSupervisedRepositories(ctx context.Context, repos []config.Repo, activation config.ClientView, profiles []clientprofile.Profile, profileEvents <-chan clientprofile.Profile, ipc *ipcserver.Server, gate runtime.Gate, mutex runtime.RepoMutex) error {
	runtimes := make(map[reposupervisor.Key]repoRuntime, len(repos))
	byServer := make(map[string][]reposupervisor.Desired)
	for _, repo := range repos {
		key := reposupervisor.Key{ServerID: repo.ServerID, RepoID: repo.ID}
		state := ipc.RegisterRepoAccess(repo.ID, repo.RepoURL, repo.LocalPath, repo.ServerID, repo.Access)
		runtimes[key] = repoRuntime{config: repo, state: state}
		byServer[repo.ServerID] = append(byServer[repo.ServerID], reposupervisor.Desired{Key: key, Access: repo.Access, State: "active", URL: repo.RepoURL, DisplayName: repo.ID})
	}
	deps := readWriteDependencies{gate: gate, mutex: mutex, ipc: ipc}
	starter := &daemonRepoStarter{daemonCtx: ctx, repos: runtimes, newSVN: func(repo config.Repo) client.Client {
		return client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:" + repo.ID, SSHIdentityFile: repo.SSHIdentityFile, SSHKnownHosts: repo.SSHKnownHosts})
	}}
	starter.startReadWrite = func(lifecycle context.Context, runtimeRepo repoRuntime, svn client.Client, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
		return startReadWrite(lifecycle, runtimeRepo, svn, desired, deps)
	}
	supervisor, err := reposupervisor.New(starter, nil)
	if err != nil {
		return err
	}
	updates := make(chan projectionUpdate, 16)
	monitored := make(map[string]bool)
	startMonitor := func(serverID, displayName, identityFile, knownHosts string, sshPort int, serviceURL string, sync clientview.SyncConfig, interval time.Duration) error {
		if monitored[serverID] {
			return nil
		}
		cached, exists, err := clientview.CachedOrNone(sync.CachePath)
		if err != nil {
			return fmt.Errorf("load cached projection: %w", err)
		}
		if exists {
			if err := reconcileProjectedView(ctx, supervisor, serverID, cached, runtimes); err != nil {
				return fmt.Errorf("apply cached projection: %w", err)
			}
		}
		monitored[serverID] = true
		ipc.RegisterActivation(contract.ActivationStatus{ServerID: serverID, DisplayName: displayName, ClientRole: "normal"})
		svn := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:projection:" + serverID, SSHIdentityFile: identityFile, SSHKnownHosts: knownHosts, SSHPort: sshPort})
		var updater clientview.Updater = svn
		if serviceURL != "" {
			updater = serviceProjectionUpdater{client: svn, url: serviceURL}
		}
		views := clientview.Monitor(ctx, updater, clientview.MonitorConfig{Sync: sync, Interval: interval, OnError: func(err error) { talk.With("projection:"+serverID).Warnf("sync failed: %v", err) }})
		go func() {
			for view := range views {
				select {
				case updates <- projectionUpdate{serverID: serverID, displayName: displayName, view: view}:
				case <-ctx.Done():
					return
				}
			}
		}()
		return nil
	}
	startProfile := func(profile clientprofile.Profile) error {
		return startMonitor(profile.ServerID, profile.DisplayName, profile.IdentityFile, profile.KnownHosts, profile.SSHPort, profile.ServiceURL, clientview.SyncConfig{WorkingCopy: profile.ServiceWC, RelativeViewPath: profile.RelativeViewPath, CachePath: profile.CachePath}, profile.PollInterval)
	}
	for _, profile := range profiles {
		if err := startProfile(profile); err != nil {
			talk.With("projection:"+profile.ServerID).Errorf("restore profile: %v", err)
		}
	}
	if projection := activation.Projection; projection != nil && !monitored[activation.ServerID] {
		if err := startMonitor(activation.ServerID, activation.DisplayName, activation.IdentityFile, activation.KnownHosts, 0, "", clientview.SyncConfig{WorkingCopy: projection.WorkingCopy, RelativeViewPath: projection.RelativeViewPath, CachePath: projection.CachePath}, projection.Interval); err != nil {
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
		case update := <-updates:
			ipc.RegisterActivation(contract.ActivationStatus{ServerID: update.serverID, DisplayName: update.displayName, ClientRole: update.view.ClientRole})
			if err := reconcileProjectedView(ctx, supervisor, update.serverID, update.view, runtimes); err != nil && ctx.Err() == nil {
				talk.With("projection:"+update.serverID).Errorf("reconcile generation %d: %v", update.view.Generation, err)
			}
		}
	}
}
