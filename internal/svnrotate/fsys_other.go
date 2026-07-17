//go:build !unix

package svnrotate

import "errors"

func acquireLock(string) (func(), error) {
	return nil, errors.New("filees-rotate is only supported on unix systems")
}

func sameFilesystem(string, string) (bool, error) {
	return false, errors.New("filees-rotate is only supported on unix systems")
}
