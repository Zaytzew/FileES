package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	"filees/pkg/errmap"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

func main() {
	lg := talk.With("filees")

	// Load configuration from ./config.json (cwd)
	cfgPath := "config.json"
	repos, err := config.Load(cfgPath)
	if err != nil {
		lg.Errorf("config: %v", err)
		os.Exit(1)
	}
	if len(repos) == 0 {
		lg.Warnf("no repositories configured in %s", cfgPath)
		return
	}

	// Root context with OS signal cancellation
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create a client shared per process (stateless across repos)
	cli := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn"})

	var wg sync.WaitGroup

	// Launch one pipeline per repo
	for _, r := range repos {
		wc := r.LocalPath
		scope := "repo:" + r.ID
		rlg := talk.With(scope)

		// Ensure FileES dirs exist
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

		// --- OnStart hygiene ---
		// If WC exists and is an SVN working copy, do a gentle cleanup+update
		if _, err := os.Stat(filepath.Join(wc, ".svn")); err == nil {
			if out, err := cli.Cleanup(ctx, wc, r.Username, r.Password); err != nil {
				rlg.Warnf("svn cleanup failed: %v %s", err, out)
			} else {
				rlg.Debugf("svn cleanup ok")
			}
			if out, err := cli.Update(ctx, wc, r.Username, r.Password); err != nil {
				rlg.Warnf("svn update failed: %v %s", err, out)
			} else {
				rlg.Debugf("svn update ok")
			}
		}

		// Promote baseline if signaled and tmp exists
		if fileExists(baselineOK) && fileExists(tmpManifest) && !fileExists(manifest) {
			if err := os.Rename(tmpManifest, manifest); err != nil {
				rlg.Warnf("promote manifest failed: %v", err)
			} else {
				_ = os.Remove(baselineOK)
				rlg.Infof("PROMOTE baseline → active (onstart)")
			}
		}

		// Stale busy check (warn only; watcher also protects with TTL)
		if fi, err := os.Stat(busyPath); err == nil {
			if time.Since(fi.ModTime()) > 10*time.Minute {
				rlg.Warnf("commit.busy appears stale (>10m) — will be ignored by watcher")
			}
		}

		// Watcher options per canon
		win := r.CommitInterval
		if win <= 0 { win = 30 * time.Second }
		scanPeriod := win / 2
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

		// Przelicz tiery rozmiaru z config (MiB float → bajty int64)
		sizeTiers := make([]commit.SizeTier, len(r.CommitTiers))
		for i, t := range r.CommitTiers {
			sizeTiers[i] = commit.SizeTier{
				MaxBytes: int64(t.MaxMB * 1024 * 1024),
				Interval: t.Interval,
			}
		}

		// HEAD poll interval: use configured value or default 30s
		pollInterval := r.PollInterval
		if pollInterval <= 0 { pollInterval = 30 * time.Second }

		// Commit service wiring
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
		// Stable client UUID (per working copy)
		clientUUID := loadOrCreateUUID(filepath.Join(stateDir, "client.uuid"))
		rlg.Debugf("client UUID: %s", clientUUID)

		// Error log sink — append to <wc>/.filees/logs/errors.jsonl
		var sink *errmap.Sink
		errLogPath := filepath.Join(logsDir, "errors.jsonl")
		if f, ferr := os.OpenFile(errLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr != nil {
			rlg.Warnf("cannot open error log %s: %v — structured errors disabled", errLogPath, ferr)
		} else {
			sink = errmap.NewSink(f, "commit:"+r.ID)
		}

		svc := &commit.Service{
			Cli:      cli,
			Tickets:  nil, // TODO: wire real tickets client if/when available
			Rules:    rules,
			HostGate: nil, // TODO: wire runtime gate if desired
			RepoMtx:  nil, // TODO: wire per-repo mutex if desired
			Logger:   talk.With("commit:" + r.ID),
			RepoURL:  r.RepoURL,
			UUID:     clientUUID,
			ErrSink:  sink,
		}

		// Start pipeline per repo
		wg.Add(1)
		go func(repo config.Repo) {
			defer wg.Done()
			events := scn.Start(ctx)
			svc.Run(ctx, repo.ID, repo.LocalPath, repo.Username, repo.Password, events)
		}(r)
	}

	// Block until signal
	<-ctx.Done()
	lg.Infof("shutdown")
	wg.Wait()
}

func max(a, b int) int { if a > b { return a }; return b }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// loadOrCreateUUID reads a stable UUID from path, generating and persisting one if absent.
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

