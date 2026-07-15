package main

import (
	"fmt"
	"os"

	"filees/pkg/config"
)

func cmdConfigCheck(args []string) int {
	path, rest := parseConfigFlag(args)
	if len(rest) != 0 {
		fmt.Fprintf(os.Stderr, "config-check: unexpected arguments: %v\n", rest)
		return 2
	}
	repos, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config-check: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "config ok: %d repositories (%s)\n", len(repos), path)
	return 0
}
