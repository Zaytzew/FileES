//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || solaris

package storage

import "golang.org/x/sys/unix"

func Available(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return product(uint64(stat.Bavail), uint64(stat.Bsize)), nil
}
