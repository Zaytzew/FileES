//go:build !unix

package svnrotate

import "errors"

func acquireLock(string) (func(), error) {
	return nil, errors.New("filees-rotate is only supported on unix systems")
}

func sameFilesystem(string, string) (bool, error) {
	return false, errors.New("filees-rotate is only supported on unix systems")
}

// rotationSupported reports whether this platform provides the advisory lock
// and filesystem check filees-rotate needs. Tests ask it instead of listing
// platforms, the same way pkg/runtime asks ownerFileLocksSupported.
func rotationSupported() bool { return false }

// Supported reports whether this platform provides the advisory lock and
// same-filesystem check filees-rotate needs. Exported so callers' tests can
// ask the package what it can do instead of listing platforms themselves.
func Supported() bool { return false }
