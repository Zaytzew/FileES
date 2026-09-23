//go:build !windows

package main

import (
	"fmt"
	"os"
)

// Only Windows has two installable variants of the desktop client.
func cmdReplacePredecessor([]string) int {
	fmt.Fprintln(os.Stderr, "replace-predecessor: only the Windows client has an MSI and a Microsoft Store variant")
	return 1
}
