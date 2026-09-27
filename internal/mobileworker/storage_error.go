package mobileworker

import (
	"errors"
	"strings"
	"syscall"
)

// ErrStorageFull is safe on the wire; detailed paths stay in the local log.
var ErrStorageFull = errors.New("server storage is full")

// IsStorageFull recognizes typed filesystem and classified SVN capacity errors.
func IsStorageFull(err error) bool {
	return errors.Is(err, ErrStorageFull) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// Only classify numeric error lines emitted by SVN, never a filename or an
// arbitrary occurrence of English text. Inspect before diagnostic truncation.
func svnStorageFull(stderr string) bool {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "svn: E000028:") {
			return true
		}
	}
	return false
}
