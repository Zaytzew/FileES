//go:build linux

package storagewatch

import "golang.org/x/sys/unix"

func (Native) Device(path string) (uint64, error) {
	var s unix.Stat_t
	err := unix.Stat(path, &s)
	return uint64(s.Dev), err
}
func (Native) Sample(path string) (Sample, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return Sample{}, err
	}
	available := s.Bavail
	if available > s.Blocks {
		available = 0
	}
	free := s.Ffree
	if free > s.Files {
		free = 0
	}
	return Sample{product(s.Blocks, uint64(s.Bsize)), product(available, uint64(s.Bsize)), s.Files, free}, nil
}
