package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/config"
)

func cmdLock(args []string) int   { return doLockUnlock(true, args) }
func cmdUnlock(args []string) int { return doLockUnlock(false, args) }

func doLockUnlock(lock bool, args []string) int {
	cfgPath, rest := parseConfigFlag(args)
	op := "lock"
	if !lock { op = "unlock" }

	if len(rest) == 0 {
		fmt.Fprintf(os.Stderr, "usage: filees %s [--config path] <file>...\n", op)
		return 1
	}

	repos, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}

	cli := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Second})
	ctx := context.Background()

	// Resolve each path to absolute, group by repo
	byRepo := make(map[string][]string) // repoID -> abs paths
	var unknown []string
	for _, arg := range rest {
		abs, err := filepath.Abs(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve %s: %v\n", arg, err)
			return 1
		}
		r := repoForPath(repos, abs)
		if r == nil {
			unknown = append(unknown, abs)
			continue
		}
		byRepo[r.ID] = append(byRepo[r.ID], abs)
	}
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "not under any configured repo:\n")
		for _, p := range unknown {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		return 1
	}

	code := 0
	for i := range repos {
		r := &repos[i]
		paths, ok := byRepo[r.ID]
		if !ok { continue }
		var out string
		if lock {
			out, err = cli.Lock(ctx, r.LocalPath, paths, r.Username, r.Password)
		} else {
			out, err = cli.Unlock(ctx, r.LocalPath, paths, r.Username, r.Password)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "svn %s: %v\n", op, err)
			if out != "" { fmt.Fprintln(os.Stderr, strings.TrimSpace(out)) }
			code = 1
		} else if out != "" {
			fmt.Print(out)
		}
	}
	return code
}

// repoForPath returns the repo whose LocalPath is the longest prefix of absPath.
func repoForPath(repos []config.Repo, absPath string) *config.Repo {
	var best *config.Repo
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
