// filees-rotate performs an explicit SVN repository generation change:
// implementation notes (not distributed) It is designed to run from cron;
// most runs end at the trigger check without touching the repository.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"filees/internal/storagewatch"
	"filees/internal/svnrotate"
	"filees/pkg/serverconfig"
)

func main() {
	configPath := flag.String("config", "/etc/filees/server.json", "server configuration for persistent write admission")
	repo := flag.String("repo", "", "absolute path to the SVN hot-repository (required)")
	archive := flag.String("archive", "", "archive directory, same filesystem as the repo (required)")
	size := flag.String("size", "25GiB", "rotate when packed repo size reaches this (KiB/MiB/GiB/TiB or bytes)")
	ageDays := flag.Int("age-days", 365, "rotate when the oldest revision's svn:date is older than this many days")
	dryRun := flag.Bool("dry-run", false, "evaluate trigger conditions and print the decision without rotating")
	force := flag.Bool("force", false, "rotate regardless of trigger conditions")
	breakLocks := flag.Bool("break-locks", false, "proceed despite active locks (destroys live edit passports)")
	dumpDepth := flag.Int("dump-depth", 0, "additionally archive a gzip dump of the last N revisions before HEAD (0 = no dump)")
	flag.Parse()

	if *repo == "" || *archive == "" {
		fmt.Fprintln(os.Stderr, "filees-rotate: -repo and -archive are required")
		os.Exit(2)
	}
	sizeBytes, err := svnrotate.ParseSize(*size)
	if err != nil {
		fmt.Fprintf(os.Stderr, "filees-rotate: -size: %v\n", err)
		os.Exit(2)
	}
	if *ageDays <= 0 {
		fmt.Fprintf(os.Stderr, "filees-rotate: -age-days must be positive, got %d\n", *ageDays)
		os.Exit(2)
	}
	if *dumpDepth < 0 {
		fmt.Fprintf(os.Stderr, "filees-rotate: -dump-depth must be >= 0, got %d\n", *dumpDepth)
		os.Exit(2)
	}

	cfg := svnrotate.Config{
		RepoPath:      *repo,
		ArchiveDir:    *archive,
		SizeThreshold: sizeBytes,
		MaxAge:        time.Duration(*ageDays) * 24 * time.Hour,
		BreakLocks:    *breakLocks,
		DumpDepth:     *dumpDepth,
	}

	var gateRoot string
	if !*dryRun {
		c, err := serverconfig.LoadFor(*configPath, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "filees-rotate: server configuration: %v\n", err)
			os.Exit(1)
		}
		gateRoot = c.Activation.Root
	}
	run := func() error { return runRotation(cfg, *dryRun, *force, gateRoot) }
	if !*dryRun {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := (storagewatch.Gate{Root: gateRoot}).WithWriter(ctx, run); err != nil {
			fmt.Fprintf(os.Stderr, "filees-rotate: %v\n", err)
			os.Exit(1)
		}
	} else if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "filees-rotate: %v\n", err)
		os.Exit(1)
	}
}

func runRotation(cfg svnrotate.Config, dryRun, force bool, gateRoot string) error {
	if err := confineRotation(cfg.RepoPath, cfg.ArchiveDir, gateRoot); err != nil {
		return fmt.Errorf("rotation sandbox: %w", err)
	}

	if !dryRun {
		if _, found, err := svnrotate.Recover(cfg.RepoPath, cfg.ArchiveDir, ""); err != nil {
			return err
		} else if found {
			fmt.Println("filees-rotate: interrupted generation recovered")
			return nil
		}
	}
	reason := "forced"
	if !force {
		rotate, why, err := svnrotate.ShouldRotate(cfg, os.Stdout)
		if err != nil {
			return err
		}
		if !rotate {
			fmt.Println("filees-rotate: no rotation needed")
			return nil
		}
		fmt.Printf("filees-rotate: trigger: %s\n", why)
		if dryRun {
			return nil
		}
		reason = why
	} else if dryRun {
		fmt.Println("filees-rotate: -force with -dry-run: would rotate unconditionally")
		return nil
	} else {
		fmt.Println("filees-rotate: forced rotation")
	}

	return svnrotate.Rotate(cfg, reason, os.Stdout)
}
