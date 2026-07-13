package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"filees/pkg/config"
)

func cmdStatus(args []string) int {
	cfgPath, _ := parseConfigFlag(args)
	repos, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	if len(repos) == 0 {
		fmt.Println("no repositories configured")
		return 0
	}
	for i := range repos {
		printRepoStatus(&repos[i])
	}
	return 0
}

func printRepoStatus(r *config.Repo) {
	wc := r.LocalPath
	stateDir := filepath.Join(wc, ".filees", "state")

	fmt.Printf("repo: %s\n", r.ID)
	fmt.Printf("  url:      %s\n", r.RepoURL)
	fmt.Printf("  local:    %s\n", wc)

	rev := strings.TrimSpace(readStr(filepath.Join(stateDir, "head.rev")))
	if rev == "" {
		fmt.Printf("  revision: —\n")
	} else {
		fmt.Printf("  revision: %s\n", rev)
	}

	n := countCacheEntries(filepath.Join(wc, ".filees", "commit_cache", "cache.json"))
	if n == 0 {
		fmt.Printf("  staged:   clean\n")
	} else {
		fmt.Printf("  staged:   %d file(s) pending commit\n", n)
	}

	busyPath := filepath.Join(stateDir, "commit.busy")
	if fi, err := os.Stat(busyPath); err == nil {
		age := time.Since(fi.ModTime()).Round(time.Second)
		fmt.Printf("  commit:   in progress (%s ago)\n", age)
	}

	pidPath := filepath.Join(stateDir, "daemon.pid")
	if pid := readInt(pidPath); pid > 0 {
		if processAlive(pid) {
			fmt.Printf("  daemon:   running (pid %d)\n", pid)
		} else {
			fmt.Printf("  daemon:   stopped (stale pid %d)\n", pid)
		}
	} else {
		fmt.Printf("  daemon:   not running\n")
	}

	if e := tailLastError(filepath.Join(wc, ".filees", "logs", "errors.jsonl")); e != "" {
		fmt.Printf("  last err: %s\n", e)
	}

	fmt.Println()
}

// countCacheEntries returns the number of items in cache.json without loading structs.
func countCacheEntries(cachePath string) int {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return 0
	}
	var raw []json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return 0
	}
	return len(raw)
}

type errLineShort struct {
	TS       string `json:"ts"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Msg      string `json:"msg"`
}

// tailLastError returns a one-line summary of the last error log entry.
func tailLastError(logPath string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var last string
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			last = t
		}
	}
	if last == "" {
		return ""
	}
	var e errLineShort
	if json.Unmarshal([]byte(last), &e) != nil {
		return last
	}
	ts := e.TS
	if len(ts) > 19 { ts = ts[:19] }
	return fmt.Sprintf("[%s] %s %s %s", ts, e.Severity, e.Code, e.Msg)
}

func readStr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readInt(path string) int {
	s := strings.TrimSpace(readStr(path))
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
