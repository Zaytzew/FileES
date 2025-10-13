package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// wewnętrzne moduły
	"filees/pkg/talk"
	"filees/pkg/config"
	"filees/pkg/watcher"
	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/runtime"
	"filees/pkg/tickets"
)

func main() {
	// --- CLI / flags ---
	cfgPath := flag.String("config", "config.json", "Ścieżka do pliku konfiguracyjnego (JSON)")
	logLevel := flag.String("log", "", "Poziom logów: silent|error|warn|info|debug|trace (opcjonalne, nadpisuje env)")
	flag.Parse()

	// --- LOG / talk ---
	if *logLevel != "" {
		talk.SetLevelString(*logLevel)
	}
	lg := talk.With("main")

	// --- CONFIG ---
	cfgs, err := config.Load(*cfgPath)
	if err != nil {
		lg.Errorf("Nie udało się wczytać configu: %v", err)
		os.Exit(1)
	}
	if len(cfgs) == 0 {
		lg.Warnf("Brak wpisów w configu (%s) — nic do zrobienia", *cfgPath)
		return
	}

	// Ustal bramkę host-wide (globalne sloty commitów na hoście)
	maxSlots := pickGlobalSlots(cfgs) // domyślnie 3 jeśli 0
	hostGate := runtime.NewHostGate(maxSlots)
	lg.Infof("Host-wide commit slots = %d", maxSlots)

	// --- Context + sygnały ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		talk.Warnf("Odebrano sygnał: %s — kończę...", s.String())
		cancel()
	}()

	// --- Start repo workers ---
	type repoRuntime struct {
		id string
	}
	done := make(chan repoRuntime, len(cfgs))

	for i := range cfgs {
		rc := cfgs[i] // kopia na iterację
		go func(c config.Repo) {
			repolog := talk.With("repo:"+c.ID, [2]string{"wc", c.LocalPath})
			repolog.Infof("Start: %s → %s", c.RepoURL, c.LocalPath)

			// SVN klient (exec CLI)
			cli := client.New(client.Options{
				SvnPath:  "svn",
				Timeout:  5 * time.Minute,
				LogScope: "svn:" + c.ID,
			})

			// Warstwa tickets (.filees/tickets/*.req)
			tix := tickets.New()

			// Zasady commitów
			rules := commit.Rules{
				Window:         pickWindow(c),           // AUTOCOMMIT_WINDOW
				MaxBatchFiles:  pickMaxBatch(c),        // liczba plików
				ShoutPatterns:  config.MustCompileRegex(c.ShoutPatterns),
				LockFirst:      c.LockFirst,
				RateLimitShout: pickShoutRate(c),
			}

			// Repo-mutex (opcjonalny, lokalny na host — „jeden commit naraz do repo”)
			repoMtx := runtime.NewRepoMutex()

			// Watcher: skan co połowę okna; mtime→size→md5 (bez inotify)
			stateDir := filepath.Join(c.LocalPath, ".filees", "state")
			_ = os.MkdirAll(stateDir, 0o755)
			scanPeriod := rules.Window / 2
			if scanPeriod <= 0 {
				scanPeriod = 15 * time.Second
			}
			sc, err := watcher.NewScanner(watcher.Options{
				WC:         c.LocalPath,
				StatePath:  filepath.Join(stateDir, "manifest.json"),
				ScanPeriod: scanPeriod,
				LogScope:   "watch:" + c.ID,
			})
			if err != nil {
				repolog.Errorf("Watcher init failed: %v", err)
				done <- repoRuntime{id: c.ID}
				return
			}
			events := sc.Start(ctx)

			// Service: staging + decyzje + svn + tickets + gates
			svc := commit.Service{
				Cli:      cli,
				Tickets:  tix,
				Rules:    rules,
				HostGate: hostGate,   // limit 2–3 aktywnych commitów na hoście
				RepoMtx:  repoMtx,    // mutex per repo (lokalny)
				Logger:   talk.With("commit:" + c.ID),
				RepoURL:  c.RepoURL, 
			}

			// Checkout/Update (jeśli potrzeba) — „shellowo”, ale safer
			if err := ensureWorkingCopy(ctx, cli, c); err != nil {
				repolog.Errorf("WC init/update failed: %v", err)
				done <- repoRuntime{id: c.ID}
				return
			}

			// Pętla serwisowa repo (blokująca do ctx.Done)
			svc.Run(ctx, c.ID, c.LocalPath, c.Username, c.Password, events)

			repolog.Infof("Stop")
			done <- repoRuntime{id: c.ID}
		}(rc)
	}

	// --- Czekaj na wszystkie repo ---
	waitLeft := len(cfgs)
	for waitLeft > 0 {
		select {
		case <-ctx.Done():
			// Po sygnale/control-c wyjdziemy, gdy wątki repo zakończą Run()
		case <-done:
			waitLeft--
		}
	}
	talk.Infof("Zakończono pracę (%d repo)", len(cfgs))
}

// --- helpers ---

func pickGlobalSlots(cfgs []config.Repo) int {
	// Wybierz największą wartość z configów; jeśli 0 → domyślnie 3
	max := 0
	for _, c := range cfgs {
		if c.GlobalSlots > max {
			max = c.GlobalSlots
		}
	}
	if max <= 0 {
		max = 3
	}
	return max
}

func pickWindow(c config.Repo) time.Duration {
	if d := c.CommitInterval; d > 0 {
		return d
	}
	// fallback na 30s gdy brak
	return 30 * time.Second
}

func pickMaxBatch(c config.Repo) int {
	if c.MaxBatchFiles > 0 {
		return c.MaxBatchFiles
	}
	return 1000
}

func pickShoutRate(c config.Repo) time.Duration {
	if c.RateLimitShout > 0 {
		return c.RateLimitShout
	}
	// domyślnie 4h jak w DSR
	return 4 * time.Hour
}

// Ensure WC present & up-to-date (bez magii – jak shell)
func ensureWorkingCopy(ctx context.Context, cli client.Client, c config.Repo) error {
	// jeśli brak .svn → checkout; w przeciwnym razie: cleanup + update
	if _, err := os.Stat(filepath.Join(c.LocalPath, ".svn")); os.IsNotExist(err) {
		if err := os.MkdirAll(c.LocalPath, 0o755); err != nil {
			return err
		}
		_, err = cli.Checkout(ctx, c.RepoURL, c.LocalPath, c.Username, c.Password)
		return err
	}
	// cleanup (best-effort)
	_, _ = cli.Cleanup(ctx, c.LocalPath)
	// update
	_, err := cli.Update(ctx, c.LocalPath, c.Username, c.Password)
	return err
}

