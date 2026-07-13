package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"filees/pkg/config"
)

func cmdLog(args []string) int {
	cfgPath, rest := parseConfigFlag(args)
	n := 20
	if len(rest) > 0 {
		if parsed, err := strconv.Atoi(rest[0]); err == nil && parsed > 0 {
			n = parsed
		}
	}

	repos, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	if len(repos) == 0 {
		fmt.Println("no repositories configured")
		return 0
	}

	for _, r := range repos {
		logPath := filepath.Join(r.LocalPath, ".filees", "logs", "errors.jsonl")
		lines := tailFile(logPath, n)
		if len(lines) == 0 {
			fmt.Printf("repo %s: no errors logged\n\n", r.ID)
			continue
		}
		fmt.Printf("--- repo: %s ---\n", r.ID)
		for _, l := range lines {
			printLogLine(l)
		}
		fmt.Println()
	}
	return 0
}

type fullErrLine struct {
	TS       string `json:"ts"`
	Scope    string `json:"scope"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Hint     string `json:"hint"`
	Msg      string `json:"msg"`
	Details  string `json:"details"`
}

func printLogLine(raw string) {
	var e fullErrLine
	if json.Unmarshal([]byte(raw), &e) != nil {
		fmt.Println(raw)
		return
	}
	ts := e.TS
	if len(ts) > 19 { ts = ts[:19] }
	details := ""
	if e.Details != "" {
		d := e.Details
		if len(d) > 80 { d = d[:77] + "..." }
		details = "  | " + d
	}
	fmt.Printf("[%s] %-5s %-12s %s%s\n", ts, e.Severity, e.Code, e.Msg, details)
}

// tailFile reads path and returns the last n non-empty lines.
func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil { return nil }
	defer f.Close()

	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) <= n { return lines }
	return lines[len(lines)-n:]
}
