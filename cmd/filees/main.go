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
	"filees/pkg/errmap"
	"filees/pkg/ipcserver"
	"filees/pkg/runtime"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "status":
			os.Exit(cmdStatus(os.Args[2:]))
		case "lock":
			os.Exit(cmdLock(os.Args[2:]))
		case "unlock":
			os.Exit(cmdUnlock(os.Args[2:]))
		case "log":
			os.Exit(cmdLog(os.Args[2:]))
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
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cli := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn"})
	gate := runtime.NewHostGate(3)
	mtx := runtime.NewRepoMutex()

	// IPC contract server
	ipc := ipcserver.New(ipcserver.DefaultSocketPath())
	if err := ipc.Start(ctx); err != nil {
		lg.Warnf("ipc: cannot start contract server: %v — CLI commands will use file fallback", err)
	}

	var wg sync.WaitGroup
	var pidPaths []string

	for _, r := range repos {
		wc := r.LocalPath
		scope := "repo:" + r.ID
		rlg := talk.With(scope)

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

		// Register repo in IPC server; rs is updated by daemon goroutines
		rs := ipc.RegisterRepo(r.ID, r.RepoURL, wc)

		if _, err := os.Stat(filepath.Join(wc, ".svn")); err == nil {
			if out, err := cli.Cleanup(ctx, wc, r.Username, r.Password); err != nil {
				rlg.Warnf("svn cleanup failed: %v %s", err, out)
			}
			if out, err := cli.Update(ctx, wc, r.Username, r.Password); err != nil {
				rlg.Warnf("svn update failed: %v %s", err, out)
			}
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

		win := r.CommitInterval
		if win <= 0 { win = 30 * time.Second }
		scanPeriod := r.WatchInterval
		if scanPeriod <= 0 { scanPeriod = win / 2 }
		wopts := watcher.Options{
			WC:              wc,
			StatePath:       manifest,
			ScanPeriod:      scanPeriod,
			BusyPath:        busyPath,
			BusyTTL:         10 * time.Minute,
			TicketsPoll:     12 * time.Second,
			DeletedDebounce: 10 * time.Minute,
			LogScope:        "watch:" + r.ID,
			UseMD5:          true,
			ChanSize:        1024,
		}
		scn, err := watcher.NewScanner(wopts)
		if err != nil {
			rlg.Errorf("watcher: %v", err)
			continue
		}

		sizeTiers := make([]commit.SizeTier, len(r.CommitTiers))
		for i, t := range r.CommitTiers {
			sizeTiers[i] = commit.SizeTier{
				MaxBytes: int64(t.MaxMB * 1024 * 1024),
				Interval: t.Interval,
			}
		}

		pollInterval := r.PollInterval
		if pollInterval <= 0 { pollInterval = 30 * time.Second }

		rules := commit.Rules{
			Window:         win,
			MaxBatchFiles:  max(1, r.MaxBatchFiles),
			ShoutPatterns:  config.MustCompileRegex(r.ShoutPatterns),
			LockFirst:      r.LockFirst,
			RateLimitShout: r.RateLimitShout,
			NewLatency:     5 * time.Minute,
			SizeTiers:      sizeTiers,
			PollInterval:   pollInterval,
		}

		clientUUID := loadOrCreateUUID(filepath.Join(stateDir, "client.uuid"))
		rlg.Debugf("client UUID: %s", clientUUID)

		var sink *errmap.Sink
		errLogPath := filepath.Join(logsDir, "errors.jsonl")
		if f, ferr := os.OpenFile(errLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr != nil {
			rlg.Warnf("cannot open error log %s: %v — structured errors disabled", errLogPath, ferr)
		} else {
			sink = errmap.NewSink(f, "commit:"+r.ID)
		}

		svc := &commit.Service{
			Cli:      cli,
			Tickets:  nil,
			Rules:    rules,
			HostGate: gate,
			RepoMtx:  mtx,
			Logger:   talk.With("commit:" + r.ID),
			RepoURL:  r.RepoURL,
			UUID:     clientUUID,
			ErrSink:  sink,
			OnConnectivity: func(state string) {
				if state == "offline" {
					rs.SetConnectivity(contract.ConnOffline)
					rs.SetState(contract.StateOffline)
				} else {
					rs.SetConnectivity(contract.ConnOnline)
					rs.SetState(contract.StateActive)
				}
			},
		}

		wg.Add(1)
		go func(repo config.Repo, repoState *ipcserver.RepoState) {
			defer wg.Done()
			repoState.SetState(contract.StateActive)
			events := scn.Start(ctx)
			svc.Run(ctx, repo.ID, repo.LocalPath, repo.Username, repo.Password, events)
			repoState.SetState(contract.StateStopping)
		}(r, rs)
	}

	<-ctx.Done()
	lg.Infof("shutdown")
	wg.Wait()

	for _, p := range pidPaths {
		_ = os.Remove(p)
	}
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
  status    show sync state for all configured repos
  lock      lock file(s) in SVN repository
  unlock    release SVN lock on file(s)
  log [N]   show last N error log entries (default 20)

flags:
  --config path   path to config.json (default: ./config.json)`)
}

// --- daemon-only helpers ---

func max(a, b int) int { if a > b { return a }; return b }

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
