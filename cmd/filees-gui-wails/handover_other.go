//go:build !windows

package main

// awaitReplacedInstance has nothing to wait for outside Windows.
//
// A restart there replaces the process image with syscall.Exec, so the old
// instance and the new one are never alive at the same time and the
// single-instance lock never sees two of them.
func awaitReplacedInstance() error { return nil }
