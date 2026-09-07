//go:build windows

package repoworker

// Server execution recovery is supported on Unix, never inferred on Windows.
func passportExecutorDead(pid int) (bool, error) { return false, nil }
