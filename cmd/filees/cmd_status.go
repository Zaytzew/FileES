package main

import (
	"context"
	"fmt"
	"os"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

func cmdStatus(args []string) int {
	_, _ = parseConfigFlag(args) // accepted but unused — status comes from daemon

	cli := ipcclient.New(ipcclient.DefaultSocketPath(), "fileesctl")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	list, err := cli.RepoList(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: start the daemon with `filees daemon`\n")
		return 1
	}
	if len(list.Repos) == 0 {
		fmt.Println("no repositories configured")
		return 0
	}

	for _, summary := range list.Repos {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		status, err := cli.RepoStatus(ctx2, summary.ID)
		cancel2()
		if err != nil {
			fmt.Printf("repo: %s — %v\n\n", summary.ID, err)
			continue
		}
		printStatus(summary, status)
	}
	return 0
}

func printStatus(s contract.RepoSummary, r *contract.RepoStatus) {
	fmt.Printf("repo: %s\n", r.RepoID)
	fmt.Printf("  url:      %s\n", s.URL)
	fmt.Printf("  local:    %s\n", s.LocalPath)
	fmt.Printf("  state:    %s  connectivity: %s\n", r.State, r.Connectivity)

	if r.LocalRevision == r.HeadRevision || r.HeadRevision == 0 {
		fmt.Printf("  revision: %d\n", r.LocalRevision)
	} else {
		fmt.Printf("  revision: %d (server: %d) — behind\n", r.LocalRevision, r.HeadRevision)
	}

	total := r.Pending.Added + r.Pending.Modified + r.Pending.Deleted
	if total == 0 {
		fmt.Printf("  staged:   clean\n")
	} else {
		fmt.Printf("  staged:   %d (A:%d M:%d D:%d)\n",
			total, r.Pending.Added, r.Pending.Modified, r.Pending.Deleted)
	}

	if r.Conflicts > 0 {
		fmt.Printf("  conflicts: %d unresolved\n", r.Conflicts)
	}

	if r.LastSyncAt != "" {
		fmt.Printf("  last sync: %s\n", r.LastSyncAt)
	}

	if r.CurrentOperation != nil {
		fmt.Printf("  current:  %s\n", *r.CurrentOperation)
	}

	if rec := r.Recovery; rec.CacheResumed > 0 || rec.AlreadyAccepted > 0 || rec.CommitBatches > 0 {
		fmt.Printf("  recovery: %d batches, %d resumed from cache, %d already accepted\n", rec.CommitBatches, rec.CacheResumed, rec.AlreadyAccepted)
	}

	fmt.Println()
}
