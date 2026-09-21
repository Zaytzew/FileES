//go:build !linux && !windows

package main

import (
	"fmt"
	"os"
)

func cmdUpdateChannel(_ []string) int {
	fmt.Fprintln(os.Stderr, "update-channel: desktop self-update is unavailable on this platform")
	return 1
}
