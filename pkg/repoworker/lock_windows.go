package repoworker

import (
	"errors"
	"os"
)

func WithFileLock(path string, fn func() error) error {
	lock := path + ".exclusive"
	f, e := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return errors.New("repository worker is already active")
	}
	f.Close()
	defer os.Remove(lock)
	return fn()
}
