//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package runtime

import (
	"errors"
	"os"
)

func ownerFileLocksSupported() bool { return false }

func createLockedOwnerFile(string, []byte) (*os.File, error) {
	return nil, errors.New("owner file locks unsupported on this platform")
}

func ownerFileLockActive(string) (bool, error) {
	return false, errors.New("owner file locks unsupported on this platform")
}
