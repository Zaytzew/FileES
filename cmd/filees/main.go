package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/deploy"
	"filees/pkg/errmap"
	"filees/pkg/ipcserver"
	"filees/pkg/passport"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"filees/pkg/tickets"
	"filees/pkg/watcher"
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

	var wg sync.WaitGroup
	var pidPaths []string
	var passportSessions []*passportSession
	var errorFiles []*os.File

	for _, r := range repos {
		wc := r.LocalPath
		scope := "repo:" + r.ID
		rlg := talk.With(scope)
		cli := client.New(client.Options{
			SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:" + r.ID,
			SSHIdentityFile: r.SSHIdentityFile, SSHKnownHosts: r.SSHKnownHosts,
		})

		stateDir := filepath.Join(wc, ".filees", "state")
		ticketsDir := filepath.Join(wc, ".filees", "tickets")
		locksGlobal := filepath.Join(wc, ".filees", "locks", "global")
		locksRepo := filepath.Join(wc, ".filees", "locks", "repo")
		logsDir := filepath.Join(wc, ".filees", "logs")
		for _, d := range []string{stateDir, ticketsDir, locksGlobal, locksRepo, logsDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				rlg.Errorf("init dir %s: %v", d, err)
				continue
			}
		}

		manifest := filepath.Join(stateDir, "manifest.json")
		tmpManifest := filepath.Join(stateDir, "manifest.tmp")
		baselineOK := filepath.Join(stateDir, "baseline.ok")
		busyPath := filepath.Join(stateDir, "commit.busy")
		pidPath := filepath.Join(stateDir, "daemon.pid")

		// Write PID so `filees status` can detect running daemon
		_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
		pidPaths = append(pidPaths, pidPath)

		clientUUID := loadOrCreateUUID(filepath.Join(stateDir, "client.uuid"))
		rlg.Debugf("client UUID: %s", clientUUID)

		// The access mode comes from the daemon's cached server projection. A
		// read-only repo never receives watcher, commit or edit-passport wiring.
		rs := ipc.RegisterRepoAccess(r.ID, r.RepoURL, wc, r.ServerID, r.Access)
		if r.Access == contract.AccessReadOnly {
			wg.Add(1)
			go func(repo config.Repo, repoState *ipcserver.RepoState, svn client.Client) {
				defer wg.Done()
				runReadOnlyRepo(ctx, repo, repoState, svn, talk.With("readonly:"+repo.ID))
			}(r, rs, cli)
			continue
		}

		var editPassports *passport.Manager
		if r.EditPassports {
			backend := passport.SVNBackend{Client: cli, WC: wc}
			editPassports, err = passport.Open(
				filepath.Join(wc, ".filees", "passports", "passports.json"),
				clientUUID,
				backend,
				passport.Config{TTL: r.EditPassportTTL, HeartbeatInterval: r.EditPassportHeartbeat, MaxSession: r.EditPassportMaxSession, CloseGrace: r.EditPassportCloseGrace},
			)
			if err != nil {
				rlg.Errorf("edit passports: %v", err)
				continue
			}
			passportSession, sessionErr := startPassportSession(ctx, editPassports)
			if sessionErr != nil {
				rlg.Errorf("edit passport lifecycle: %v", sessionErr)
				continue
			}
			passportSessions = append(passportSessions, passportSession)
		}

		// Register repo in IPC server; rs is updated by daemon goroutines
		if editPassports != nil {
			rs.SetLockFuncs(
				func(ctx context.Context, paths []string) (string, error) {
					_, out, err := editPassports.Acquire(ctx, paths)
					return out, err
				},
				func(ctx context.Context, paths []string) (string, error) { return editPassports.Release(ctx, paths) },
			)
		} else {
			rs.SetLockFuncs(
				func(ctx context.Context, paths []string) (string, error) {
					return cli.Lock(ctx, wc, paths)
				},
				func(ctx context.Context, paths []string) (string, error) {
					return cli.Unlock(ctx, wc, paths)
				},
			)
		}

		if fileExists(baselineOK) && fileExists(tmpManifest) && !fileExists(manifest) {
			if err := os.Rename(tmpManifest, manifest); err != nil {
				rlg.Warnf("promote manifest failed: %v", err)
			} else {
				_ = os.Remove(baselineOK)
				rlg.Infof("PROMOTE baseline → active (onstart)")
			}
		}

		if fi, err := os.Stat(busyPath); err == nil {
			if time.Since(fi.ModTime()) > 10*time.Minute {
				rlg.Warnf("commit.busy appears stale (>10m) — will be ignored by watcher")
			}
		}

		// A disappearance must be observed no later than a new file becomes
		// publishable. Otherwise an Added entry can outlive its file in staging.
		wopts, publishLatency := buildWatcherOptions(r, manifest, busyPath)
		scn, err := watcher.NewScanner(wopts)
		if err != nil {
			rlg.Errorf("watcher: %v", err)
			continue
		}

		rules := buildCommitRules(r, publishLatency)

		var sink *errmap.Sink
		errLogPath := filepath.Join(logsDir, "errors.jsonl")
		if createdSink, file, ferr := openRepoErrorSink(errLogPath, "commit:"+r.ID); ferr != nil {
			rlg.Warnf("cannot open error log %s: %v — structured errors disabled", errLogPath, ferr)
		} else {
			sink = createdSink
			errorFiles = append(errorFiles, file)
		}

		svc := &commit.Service{
			Cli:      cli,
			Rules:    rules,
			HostGate: gate,
			RepoMtx:  mtx,
			Logger:   talk.With("commit:" + r.ID),
			RepoURL:  r.RepoURL,
			UUID:     clientUUID,
			ErrSink:  sink,
			Emit: func(evType string, payload any) {
				ipc.Emit(ipc.NewRepoEvent(r.ID, evType, payload))
			},
		}
		wireRepoStatus(svc, rs)
		if editPassports != nil {
			svc.BeginPublish = editPassports.BeginPublish
			svc.OnPathActivity = editPassports.Touch
			svc.OnPathsPublished = editPassports.MarkPublished
			svc.OnPathsRemoved = editPassports.ForgetRemoved
		}

		// Reconcile startup-update conflicts through the same lossless path as
		// periodic updates. In particular, this covers a SIGKILL after the server
		// accepted a commit but before SVN updated the working-copy metadata.
		recoverReadWriteWorkingCopy(ctx, cli, wc, svc, rlg)

		if editPassports != nil {
			if err := passport.EnsureNeedsLock(ctx, cli, wc, clientUUID, intOrDefault(r.MaxBatchFiles, 100)); err != nil {
				rlg.Errorf("edit-passport migration: %v", err)
				rs.SetState(contract.StateDegraded)
				continue
			}
		}

		wg.Add(1)
		go func(repo config.Repo, repoState *ipcserver.RepoState) {
			defer wg.Done()
			if err := runReadWritePipeline(ctx, repo, repoState, scn, svc); err != nil {
				talk.With("repo:"+repo.ID).Errorf("pipeline: %v", err)
			}
		}(r, rs)
	}

	<-ctx.Done()
	lg.Infof("shutdown")
	wg.Wait()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 35*time.Second)
	for _, session := range passportSessions {
		if err := session.Stop(shutdownCtx); err != nil {
			lg.Warnf("edit passports shutdown release: %v", err)
		}
	}
	shutdownCancel()
	for _, file := range errorFiles {
		if err := file.Close(); err != nil {
			lg.Warnf("close structured error log: %v", err)
		}
	}

	for _, p := range pidPaths {
		_ = os.Remove(p)
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
