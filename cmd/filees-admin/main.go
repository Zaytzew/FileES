package main

import (
	"fmt"
	"os"

	"filees/internal/servertool"
)

func main() {
	args := os.Args[1:]
	if adminNeedsStateIdentity(args) {
		if err := prepareAdminIdentity(); err != nil {
			fmt.Fprintf(os.Stderr, "filees-admin: prepare state identity: %v\n", err)
			os.Exit(servertool.ExitConfig)
		}
	}
	os.Exit(servertool.RunAdmin(args, os.Stdout, os.Stderr))
}

func adminNeedsStateIdentity(args []string) bool {
	return len(args) != 1 || args[0] != "version"
}
