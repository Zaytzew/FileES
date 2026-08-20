//go:build windows

package whaleworker

// Windows does not permit os.Open on a directory with the access required by
// File.Sync. The state file itself is still flushed before the atomic rename;
// OpenBSD uses syncdir_unix.go and additionally fsyncs both containing dirs.
func syncDir(string) error { return nil }
