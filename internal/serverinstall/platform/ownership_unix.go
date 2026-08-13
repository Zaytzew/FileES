//go:build linux || openbsd

package platform

import (
	"fmt"
	"os"
	"syscall"
)

func statOwnership(info os.FileInfo) (Ownership, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Ownership{}, fmt.Errorf("file metadata exposes no uid/gid")
	}
	return Ownership{UID: int(st.Uid), GID: int(st.Gid)}, nil
}
