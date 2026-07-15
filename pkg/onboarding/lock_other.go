//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package onboarding

import "errors"

func (s *Files) withLock(func() error) error {
	return errors.New("onboarding filesystem locking unsupported on this platform")
}
