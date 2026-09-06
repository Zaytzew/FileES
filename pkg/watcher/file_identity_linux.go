package watcher

import (
	"fmt"
	"golang.org/x/sys/unix"
)

// No btime -> no identity evidence. MD5 is not a replacement in native mode.
func fileIdentity(path string) string {
	var s unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &s); err != nil {
		return ""
	}
	if s.Mask&(unix.STATX_BTIME|unix.STATX_INO|unix.STATX_NLINK|unix.STATX_TYPE) != (unix.STATX_BTIME|unix.STATX_INO|unix.STATX_NLINK|unix.STATX_TYPE) || s.Mode&unix.S_IFMT != unix.S_IFREG || s.Nlink != 1 {
		return ""
	}
	return fmt.Sprintf("linux:%d:%d:%d:%d:%d", s.Dev_major, s.Dev_minor, s.Ino, s.Btime.Sec, s.Btime.Nsec)
}
