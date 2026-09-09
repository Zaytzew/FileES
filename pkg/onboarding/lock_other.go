//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package onboarding

import "errors"

func (s *Files) withLock(func() error) error {
	return errors.New("onboarding filesystem locking unsupported on this platform")
}

// fileLocksSupported reports whether this platform has the advisory locking
// withLock needs. It exists so a test can ask the product what it can do
// instead of listing platforms itself - the same shape pkg/runtime uses for
// ownerFileLocksSupported.
func fileLocksSupported() bool { return false }
