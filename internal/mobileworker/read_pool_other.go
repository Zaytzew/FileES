//go:build !linux && !openbsd

package mobileworker

import (
	"context"
	"errors"
	"filees/public-shares/storage"
	"os"
	"path/filepath"
)

// Portable read tests only; the server pool requires Linux/OpenBSD locks.
func (s *readSpool) allocate(_ context.Context, size int64) error {
	if s.maxBytes != 0 {
		return errors.New("mobile read pool requires Linux or OpenBSD")
	}
	f, err := os.CreateTemp(s.root, "filees-mobile-read-*")
	if err != nil {
		return err
	}
	check := s.checkSpace
	if check == nil {
		check = storage.RequireSpace
	}
	if err := check(filepath.Dir(f.Name()), size); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	s.File = f
	s.release = func() error { return os.Remove(f.Name()) }
	return nil
}
