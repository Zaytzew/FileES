package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"filees/pkg/client"
	"filees/pkg/clientview"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/deploy"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"filees/pkg/tickets"
)

var version = "dev"

func main() {
	if deploy.AskpassConfigured() {
		if err := deploy.RunAskpass(); err != nil {
			fmt.Fprintln(os.Stderr, "filees askpass:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version":
			fmt.Fprintln(os.Stdout, version)
			return
		case "config-check":
			os.Exit(cmdConfigCheck(os.Args[2:]))
		case "status":
			os.Exit(cmdStatus(os.Args[2:]))
		case "lock":
			os.Exit(cmdLock(os.Args[2:]))
		case "unlock":
			os.Exit(cmdUnlock(os.Args[2:]))
		case "log":
			os.Exit(cmdLog(os.Args[2:]))
		case "activate-begin":
			os.Exit(cmdActivateBegin(os.Args[2:]))
		case "activate-finish":
			os.Exit(cmdActivateFinish(os.Args[2:]))
		case "activate-resume":
			os.Exit(cmdActivateResume(os.Args[2:]))
		case "daemon":
			// fall through to daemon startup below
		case "help", "--help", "-h":
			printUsage()
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
			printUsage()
			os.Exit(1)
		}
	}

	runDaemon()
}

func runDaemon() {
	lg := talk.With("filees")

	cfgPath, _ := parseConfigFlag(os.Args[1:])
	if cfgPath == "config.json" && len(os.Args) > 1 && os.Args[1] == "daemon" {
		cfgPath, _ = parseConfigFlag(os.Args[2:])
	}
	repos, err := config.Load(cfgPath)
	if err != nil {
		lg.Errorf("config: %v", err)
		os.Exit(1)
	}
	if len(repos) == 0 {
		lg.Warnf("no repositories configured in %s", cfgPath)
	}
	clientView, err := config.LoadClientView(cfgPath)
	if err != nil {
		lg.Errorf("config client view: %v", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	gate := runtime.NewHostGate(3)
	mtx := runtime.NewRepoMutex()

	// IPC contract server
	ipc := ipcserver.New(ipcserver.DefaultSocketPath())
	ipc.RegisterActivation(contract.ActivationStatus{ServerID: clientView.ServerID, DisplayName: clientView.DisplayName, ClientRole: clientView.ClientRole})
	if err := ipc.Start(ctx); err != nil {
		lg.Warnf("ipc: cannot start contract server: %v — CLI commands will use file fallback", err)
	}
	if err := runSupervisedRepositories(ctx, repos, clientView, ipc, gate, mtx); err != nil {
		lg.Errorf("repository supervisor: %v", err)
	}
	return

}

func runSupervisedRepositories(ctx context.Context, repos []config.Repo, activation config.ClientView, ipc *ipcserver.Server, gate runtime.Gate, mutex runtime.RepoMutex) error {
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
	servers := make([]string, 0, len(byServer))
	for serverID := range byServer {
		servers = append(servers, serverID)
	}
	sort.Strings(servers)
	for _, serverID := range servers {
		if activation.Projection != nil && serverID == activation.ServerID {
			continue
		}
		if err := supervisor.Apply(ctx, serverID, 1, byServer[serverID]); err != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			_ = supervisor.Stop(stopCtx)
			return err
		}
	}
	var projected <-chan clientview.View
	if projection := activation.Projection; projection != nil {
		cached, exists, err := clientview.CachedOrNone(projection.CachePath)
		if err != nil {
			return fmt.Errorf("load cached projection: %w", err)
		}
		if exists {
			if err := reconcileProjectedView(ctx, supervisor, activation.ServerID, cached, runtimes); err != nil {
				return fmt.Errorf("apply cached projection: %w", err)
			}
		}
		updater := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:projection:" + activation.ServerID, SSHIdentityFile: activation.IdentityFile, SSHKnownHosts: activation.KnownHosts})
		projected = clientview.Monitor(ctx, updater, clientview.MonitorConfig{Sync: clientview.SyncConfig{WorkingCopy: projection.WorkingCopy, RelativeViewPath: projection.RelativeViewPath, CachePath: projection.CachePath}, Interval: projection.Interval, OnError: func(err error) { talk.With("projection:"+activation.ServerID).Warnf("sync failed: %v", err) }})
	}
	for projected != nil {
		select {
		case <-ctx.Done():
			projected = nil
		case view, ok := <-projected:
			if !ok {
				projected = nil
				continue
			}
			if err := reconcileProjectedView(ctx, supervisor, activation.ServerID, view, runtimes); err != nil && ctx.Err() == nil {
				talk.With("projection:"+activation.ServerID).Errorf("reconcile generation %d: %v", view.Generation, err)
			}
		}
	}
	if ctx.Err() == nil {
		<-ctx.Done()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	return supervisor.Stop(stopCtx)
}

func reconcileProjectedView(ctx context.Context, supervisor *reposupervisor.Supervisor, serverID string, view clientview.View, runtimes map[reposupervisor.Key]repoRuntime) error {
	items := attachedProjection(serverID, view, runtimes)
	updateProjectedRepoStates(items, runtimes)
	return supervisor.Apply(ctx, serverID, view.Generation, items)
}

func updateProjectedRepoStates(items []reposupervisor.Desired, runtimes map[reposupervisor.Key]repoRuntime) {
	for _, item := range items {
		if runtime, ok := runtimes[item.Key]; ok {
			runtime.state.SetProjection(item.URL, item.Access)
		}
	}
}

func runReadOnlyRepo(ctx context.Context, repo config.Repo, rs *ipcserver.RepoState, cli client.Client, lg talk.Logger) {
	interval := repo.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	update := func() {
		rs.SetCurrentOp(stringPtr("update"))
		defer rs.SetCurrentOp(nil)
		out, err := cli.Update(ctx, repo.LocalPath)
		if err != nil {
			if ctx.Err() == nil {
				lg.Warnf("svn update failed: %v %s", err, out)
				rs.SetConnectivity(contract.ConnOffline)
				rs.SetState(contract.StateOffline)
			}
			return
		}
		rs.SetConnectivity(contract.ConnOnline)
		rs.SetState(contract.StateActive)
		rs.SetLastSyncAt(time.Now())
	}
	rs.SetState(contract.StateActive)
	update()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			rs.SetState(contract.StateStopping)
			return
		case <-ticker.C:
			update()
		}
	}
}

func stringPtr(value string) *string { return &value }

func wireRepoStatus(svc *commit.Service, rs *ipcserver.RepoState) {
	svc.Tickets = tickets.New()
	svc.OnConnectivity = func(state string) {
		if state == "offline" {
			rs.SetConnectivity(contract.ConnOffline)
			rs.SetState(contract.StateOffline)
		} else {
			rs.SetConnectivity(contract.ConnOnline)
			rs.SetState(contract.StateActive)
		}
	}
	svc.OnHeadRevision = rs.SetHeadRev
	svc.OnLastSync = rs.SetLastSyncAt
	svc.OnConflicts = rs.SetConflicts
	svc.OnCurrentOperation = rs.SetCurrentOp
}

// --- helpers shared by subcommands ---

// parseConfigFlag extracts --config <path> from args, returns (cfgPath, remaining).
func parseConfigFlag(args []string) (cfgPath string, rest []string) {
	cfgPath = "config.json"
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			cfgPath = args[i+1]
			i++
		} else {
			rest = append(rest, args[i])
		}
	}
	return
}

func printUsage() {
	fmt.Println(`usage: filees [command] [--config path] [args...]

commands:
  daemon    start sync daemon (default when no command given)
  config-check  validate configuration without starting the daemon
  version   show client version
  status    show sync state for all configured repos
  lock      lock file(s) in SVN repository
  unlock    release SVN lock on file(s)
  log [N]   show last N error log entries (default 20)
  activate-begin   create/resume a server-scoped onboarding passport
  activate-finish  read one OTP from stdin and run activation
  activate-resume  resume an OTP-authorized activation with reconnect key

flags:
  --config path   path to config.json (default: ./config.json)`)
}

// --- daemon-only helpers ---

func intOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func mibOrDefault(value, fallback float64) int64 {
	if value <= 0 {
		value = fallback
	}
	return int64(value * 1024 * 1024)
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func loadOrCreateUUID(path string) string {
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}
	id := uuid.New().String()
	_ = os.WriteFile(path, []byte(id+"\n"), 0o644)
	return id
}
