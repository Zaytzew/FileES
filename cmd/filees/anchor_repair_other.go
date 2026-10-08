//go:build !windows || nocfapi

package main

import (
	"fmt"
	"os"
)

// Virtual-folder repair exists only in the Windows build with CFAPI.
func cmdAnchorRepair([]string) int {
	fmt.Fprintln(os.Stderr, "anchor-repair: virtual folders are unavailable in this build")
	return 1
}
