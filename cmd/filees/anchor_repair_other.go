//go:build !windows

package main

import (
	"fmt"
	"os"
)

// Virtual folders exist only in the Windows client.
func cmdAnchorRepair([]string) int {
	fmt.Fprintln(os.Stderr, "anchor-repair: only the Windows client has virtual folders")
	return 1
}
