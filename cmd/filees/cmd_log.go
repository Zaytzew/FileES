package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

func cmdLog(args []string) int {
	_, rest := parseConfigFlag(args)
	n := 20
	if len(rest) > 0 {
		if parsed, err := strconv.Atoi(rest[0]); err == nil && parsed > 0 {
			n = parsed
		}
	}

	cli := ipcclient.New(ipcclient.DefaultSocketPath(), "fileesctl")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	result, err := cli.ErrorList(ctx, contract.ErrorListPayload{Limit: n})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: start the daemon with `filees daemon`\n")
		return 1
	}

	if len(result.Errors) == 0 {
		fmt.Println("no errors logged")
		return 0
	}

	lastRepo := ""
	for _, e := range result.Errors {
		if e.RepoID != lastRepo {
			fmt.Printf("--- repo: %s ---\n", e.RepoID)
			lastRepo = e.RepoID
		}
		printErrRecord(e)
	}
	return 0
}

func printErrRecord(e contract.ErrorRecord) {
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
