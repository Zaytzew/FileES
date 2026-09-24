//go:build !windows

// filees-launch exists only for the Windows MSI installation.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "filees-launch is a Windows-only program")
	os.Exit(2)
}
