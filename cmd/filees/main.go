package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
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

	// Launch one pipeline per repo
	for _, r := range repos {
		wc := r.LocalPath
		scope := "repo:" + r.ID
		rlg := talk.With(scope)

		// Ensure .filees/state dir exists
		stateDir := filepath.Join(wc, ".filees", "state")
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			rlg.Errorf("state dir: %v", err)
			continue
		}

		// Watcher options per canon
		manifest := filepath.Join(stateDir, "manifest.json")
		busyPath := filepath.Join(stateDir, "commit.busy")
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

		// Commit service wiring
		rules := commit.Rules{
			Window:         win,
			MaxBatchFiles:  max(1, r.MaxBatchFiles),
			ShoutPatterns:  config.MustCompileRegex(r.ShoutPatterns),
			LockFirst:      r.LockFirst,
			RateLimitShout: r.RateLimitShout,
			NewLatency:     5 * time.Minute,
		}
		svc := &commit.Service{
			Cli:      cli,
			Tickets:  nil, // TODO: wire real tickets client if/when available
			Rules:    rules,
			HostGate: nil, // TODO: wire runtime gate if desired
			RepoMtx:  nil, // TODO: wire per-repo mutex if desired
			Logger:   talk.With("commit:" + r.ID),
			RepoURL:  r.RepoURL,
		}

		// Start pipeline per repo
		go func(repo config.Repo) {
			events := scn.Start(ctx)
			svc.Run(ctx, repo.ID, repo.LocalPath, repo.Username, repo.Password, events)
		} (r)
	}

	// Block until signal
	<-ctx.Done()
	lg.Infof("shutdown")
	// Give background goroutines a breath to flush best-effort
	time.Sleep(500 * time.Millisecond)
}

func max(a, b int) int { if a > b { return a }; return b }
