//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package activation

import "sync"

var processLock sync.Mutex

func withFileLock(_ string, run func() error) error {
	processLock.Lock()
	defer processLock.Unlock()
	return run()
}
