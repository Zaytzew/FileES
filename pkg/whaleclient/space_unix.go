//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || solaris

package whaleclient

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func filesystemAvailable(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(filepath.Clean(path), &stat); err != nil {
		return 0, err
	}
	return saturatingSpace(uint64(stat.Bavail), uint64(stat.Bsize)), nil
}

func saturatingSpace(blocks, size uint64) int64 {
	const max = int64(^uint64(0) >> 1)
	if size != 0 && blocks > uint64(max)/size {
		return max
	}
	return int64(blocks * size)
}
