//go:build linux || openbsd

package main

import (
	"fmt"
	"syscall"
)

// svnmucc keeps one descriptor for each put. Raise only this process's soft
// limit (inherited by svnmucc), never the administrator's hard limit, and
// never split the atomic publication to evade a resource limit.
func ensureNWCDescriptorLimit(files int) error {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return fmt.Errorf("read publication descriptor limit: %w", err)
	}
	needed := uint64(files) + 64
	if needed > limit.Max {
		return fmt.Errorf("atomic publication of %d files needs %d file descriptors; hard limit is %d; nothing committed", files, needed, limit.Max)
	}
	limit.Cur = max(limit.Cur, needed)
	// Go may raise its own soft limit at startup, then restore the caller's
	// original limit in exec. syscall.Setrlimit (even if Cur already suffices)
	// records our explicit policy so svnmucc inherits it. A raw unix syscall
	// would change only the parent and leave the child at e.g. 128.
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return fmt.Errorf("set publication descriptor soft limit to %d: %w; nothing committed", limit.Cur, err)
	}
	return nil
}
