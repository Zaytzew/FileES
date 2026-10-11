//go:build !linux && !openbsd

package main

// Windows has no POSIX per-process descriptor limit. Other supported CLI
// platforms provide the bounded soft-limit adjustment in nwc_limits_unix.go.
func ensureNWCDescriptorLimit(files int) error { return nil }
