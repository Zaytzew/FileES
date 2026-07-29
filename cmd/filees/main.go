package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"

	"filees/pkg/activity"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/deploy"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"filees/pkg/reposupervisor"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"filees/pkg/tickets"
)

var version = "dev"

const (
	daemonActionNone int32 = iota
	daemonActionShutdown
	daemonActionRestart
)

type daemonLifecycle struct {
	cancel context.CancelFunc
	action atomic.Int32
}

func (lifecycle *daemonLifecycle) Shutdown() {
	if lifecycle.action.CompareAndSwap(daemonActionNone, daemonActionShutdown) {
		lifecycle.cancel()
	}
}

func (lifecycle *daemonLifecycle) Restart() {
	if lifecycle.action.CompareAndSwap(daemonActionNone, daemonActionRestart) {
		lifecycle.cancel()
	}
}

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
		case "recovery":
			os.Exit(cmdRecovery(os.Args[2:]))
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
	profiles, err := clientprofile.List(clientprofile.DefaultRoot())
	if err != nil {
		lg.Errorf("client profiles: %v", err)
		os.Exit(1)
	}
	profileEvents := make(chan clientprofile.Profile, 8)

	gate := runtime.NewHostGate(3)
	mtx := runtime.NewRepoMutex()

	// IPC contract server
	ipc := ipcserver.New(ipcserver.DefaultSocketPath())
	lifecycle := &daemonLifecycle{cancel: cancel}
	ipc.SetSystemLifecycleService(lifecycle)
	if err := configureClientUpdate(ipc, clientView.Update, version); err != nil {
		lg.Errorf("client update: %v", err)
		os.Exit(1)
	}
	lifecycleStore, err := localrepo.Open(defaultRepositoryLifecyclePath())
	if err != nil {
		lg.Errorf("repository lifecycle: %v", err)
		os.Exit(1)
	}
	repos, err = reconcileConfiguredRepositoryLifecycle(lifecycleStore, repos)
	if err != nil {
		lg.Errorf("repository lifecycle migration: %v", err)
		os.Exit(1)
	}
	provisioningStore, err := provisioning.NewStore(defaultRepositoryProvisioningPath())
	if err != nil {
		lg.Errorf("repository provisioning: %v", err)
		os.Exit(1)
	}
	activityJournal, err := activity.Open(defaultActivityJournalPath(), activity.DefaultLimit)
	if err != nil {
		lg.Errorf("recent activity: %v", err)
		os.Exit(1)
	}
	ipc.SetActivitySource(activityJournal)
	provisionedAttachments := make(chan provisionedAttachment, 16)
	provisioner := newDaemonProvisioner(lifecycleStore, provisioningStore, profiles)
	provisioner.attachments = provisionedAttachments
	go provisioner.Run(ctx)
	realmAliases := &realmAliasService{provisioner: provisioner, cache: make(map[string]ownerLabelCache)}
	ipc.SetRealmAliasService(realmAliases)
	ipc.SetOwnerLabelResolver(realmAliases)
	ipc.SetRepositoryLifecycleService(repositoryLifecycleService{store: lifecycleStore, provisioning: provisioningStore, clientID: provisioner.ClientID, onCreate: provisioner.Enqueue, onAttach: func(request attachmentRequest) { provisioner.Enqueue(request.OperationID) }, onRelocate: provisioner.Enqueue, onDetach: provisioner.Detach})
	ipc.SetMobilePairingService(mobilePairingService{provisioner: provisioner})
	ipc.SetServerDetachService(serverDetachService{local: lifecycleStore, provisioner: provisioner, profileRoot: clientprofile.DefaultRoot()})
	ipc.SetActivationService(daemonActivationService{onActive: ipc.RegisterActivation, onProfile: func(profile clientprofile.Profile) {
		provisioner.AddProfile(profile)
		select {
		case profileEvents <- profile:
		case <-ctx.Done():
		}
	}})
	if clientView.Configured {
		ipc.RegisterActivation(contract.ActivationStatus{ServerID: clientView.ServerID, DisplayName: clientView.DisplayName, ClientRole: clientView.ClientRole})
	}
	if err := ipc.Start(ctx); err != nil {
		lg.Warnf("ipc: cannot start contract server: %v — CLI commands will use file fallback", err)
	}
	if err := runDynamicSupervisedRepositories(ctx, repos, clientView, profiles, profileEvents, provisionedAttachments, ipc, gate, mtx, activityJournal); err != nil {
		lg.Errorf("repository supervisor: %v", err)
	}
	if lifecycle.action.Load() == daemonActionRestart {
		if err := restartCurrentProcess(); err != nil {
			lg.Errorf("restart daemon: %v", err)
		}
	}
	return

}

func reconcileProjectedView(ctx context.Context, supervisor *reposupervisor.Supervisor, ipc *ipcserver.Server, serverID string, view clientview.View, runtimes map[reposupervisor.Key]repoRuntime) error {
	applyRealmOwnership(serverID, view, runtimes)
	items := attachedProjection(serverID, view, runtimes)
	if err := supervisor.ApplyWithTransition(ctx, serverID, view.Generation, items, func(item reposupervisor.Desired) {
		if runtime, ok := runtimes[item.Key]; ok {
			runtime.state.SetProjection(item.URL, item.Access)
		}
	}); err != nil {
		return err
	}
	syncProjectionKnowledge(ipc, serverID, view, runtimes)
	return nil
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
  recovery download --kit FILE.fkr --output DIR
            download verified repository archives without an active profile

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
