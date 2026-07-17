// filees-rotate performs an explicit SVN repository generation change:
// concepts/SVN_ROTATOR_CONCEPT_V2.md. It is designed to run from cron;
// most runs end at the trigger check without touching the repository.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"filees/internal/svnrotate"
)

func main() {
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

	reason := "forced"
	if !*force {
		rotate, why, err := svnrotate.ShouldRotate(cfg, os.Stdout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "filees-rotate: %v\n", err)
			os.Exit(1)
		}
		if !rotate {
			fmt.Println("filees-rotate: no rotation needed")
			return
		}
		fmt.Printf("filees-rotate: trigger: %s\n", why)
		if *dryRun {
			return
		}
		reason = why
	} else if *dryRun {
		fmt.Println("filees-rotate: -force with -dry-run: would rotate unconditionally")
		return
	} else {
		fmt.Println("filees-rotate: forced rotation")
	}

	if err := svnrotate.Rotate(cfg, reason, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "filees-rotate: %v\n", err)
		os.Exit(1)
	}
}
