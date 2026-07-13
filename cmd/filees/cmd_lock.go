package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

func cmdLock(args []string) int   { return doLockUnlock(true, args) }
func cmdUnlock(args []string) int { return doLockUnlock(false, args) }

func doLockUnlock(lock bool, args []string) int {
	_, rest := parseConfigFlag(args)
	op := "lock"
	if !lock { op = "unlock" }

	if len(rest) == 0 {
		fmt.Fprintf(os.Stderr, "usage: filees %s [--config path] <file>...\n", op)
		return 1
	}

	cli := ipcclient.New(ipcclient.DefaultSocketPath(), "fileesctl")

	// Resolve all paths to absolute, then find which repo each belongs to.
	// We query repo.list from the daemon (daemon is source of truth for repo config).
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	list, err := cli.RepoList(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: start the daemon with `filees daemon`\n")
		return 1
	}

	// Group resolved absolute paths by repoID
	byRepo := make(map[string][]string)
	var unknown []string
	for _, arg := range rest {
		abs, err := filepath.Abs(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve %s: %v\n", arg, err)
			return 1
		}
		found := repoSummaryForPath(list.Repos, abs)
		if found == nil {
			unknown = append(unknown, abs)
			continue
		}
		byRepo[found.ID] = append(byRepo[found.ID], abs)
	}
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "not under any configured repo:\n")
		for _, p := range unknown { fmt.Fprintf(os.Stderr, "  %s\n", p) }
		return 1
	}

	code := 0
	for repoID, paths := range byRepo {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
		var out string
		if lock {
			out, err = cli.Lock(ctx2, repoID, paths)
		} else {
			out, err = cli.Unlock(ctx2, repoID, paths)
		}
		cancel2()
		if err != nil {
			fmt.Fprintf(os.Stderr, "svn %s [%s]: %v\n", op, repoID, err)
			code = 1
		} else if out != "" {
			fmt.Print(out)
		}
	}
	return code
}

// repoSummaryForPath returns the RepoSummary whose LocalPath is the longest
// prefix of absPath, or nil if no repo matches.
func repoSummaryForPath(repos []contract.RepoSummary, absPath string) *contract.RepoSummary {
	var best *contract.RepoSummary
	for i := range repos {
		r := &repos[i]
		sep := string(os.PathSeparator)
		if strings.HasPrefix(absPath, r.LocalPath+sep) || absPath == r.LocalPath {
			if best == nil || len(r.LocalPath) > len(best.LocalPath) {
				best = r
			}
		}
	}
	return best
}
