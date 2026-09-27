//go:build openbsd

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
	var available, free uint64
	if s.F_bavail > 0 {
		available = uint64(s.F_bavail)
	}
	if s.F_favail > 0 {
		free = uint64(s.F_favail)
	}
	return Sample{product(s.F_blocks, uint64(s.F_bsize)), product(available, uint64(s.F_bsize)), s.F_files, free}, nil
}
