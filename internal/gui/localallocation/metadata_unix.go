//go:build linux || darwin || freebsd || openbsd || netbsd

package localallocation

import (
	"errors"
	"os"
	"syscall"
)

func probe(path string) (metadata, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return metadata{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return metadata{}, errors.New("allocation: link or special file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Blocks < 0 || (!info.IsDir() && stat.Nlink > 1) {
		return metadata{}, errors.New("allocation: unsupported metadata or shared hardlink")
	}
	return metadata{bytes: stat.Blocks * 512, directory: info.IsDir(), stamp: info.ModTime().UnixNano()}, nil
}

func openDirectory(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
