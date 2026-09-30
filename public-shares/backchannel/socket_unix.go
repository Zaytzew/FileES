//go:build !windows

package backchannel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// socketDirectory checks the filesystem boundary, without changing permissions.
// The endpoint owner and root are trusted. Other accounts may traverse the
// immediate directory only via its dedicated service group, never write it.
// A system symlink such as /var/run -> /run is resolved before ancestry checks.
func socketDirectory(path string, uid, gid uint32) error {
	if !filepath.IsAbs(path) {
		return errors.New("backchannel socket path must be absolute")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("backchannel directory must already exist: %w", err)
	}
	for first := true; ; first = false {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || (st.Uid != 0 && st.Uid != uid) {
			return fmt.Errorf("untrusted backchannel directory owner: %s", dir)
		}
		// Root-owned sticky ancestors (/tmp) cannot replace a private child.
		stickyAncestor := !first && st.Uid == 0 && info.Mode()&os.ModeSticky != 0
		if info.Mode().Perm()&0022 != 0 && !stickyAncestor {
			return fmt.Errorf("backchannel directory is writable by other accounts: %s", dir)
		}
		if first && (info.Mode().Perm()&0007 != 0 || (info.Mode().Perm()&0050 != 0 && st.Gid != gid)) {
			return fmt.Errorf("backchannel directory must be private or restricted to the socket group: %s", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func checkedSocket(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0007 != 0 {
		return nil, errors.New("backchannel endpoint must be a Unix socket without access for others")
	}
	if err := socketDirectory(path, st.Uid, st.Gid); err != nil {
		return nil, err
	}
	return info, nil
}

// ListenUnix serves only a protected, administrator-provisioned directory.
// A live or ambiguous endpoint is never removed. Cleanup checks its snapshot
// and probes for a listener, including filesystems that reuse inode numbers.
func ListenUnix(path, groupName string) (net.Listener, func(), error) {
	gid := os.Getegid()
	mode := os.FileMode(0600)
	if groupName != "" {
		group, err := user.LookupGroup(groupName)
		if err != nil {
			return nil, nil, err
		}
		gid, err = strconv.Atoi(group.Gid)
		if err != nil {
			return nil, nil, err
		}
		mode = 0660
	}
	directoryGID := uint32(gid)
	if groupName == "" {
		directoryGID = ^uint32(0) // no group access before or after bind
	}
	if err := socketDirectory(path, uint32(os.Geteuid()), directoryGID); err != nil {
		return nil, nil, err
	}
	if info, err := checkedSocket(path); err == nil {
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != uint32(os.Geteuid()) {
			return nil, nil, errors.New("backchannel socket belongs to another account")
		}
		conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, nil, errors.New("backchannel socket is already listening")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, nil, fmt.Errorf("cannot establish that backchannel socket is stale: %w", dialErr)
		}
		if err := removeOwnedSocket(path, info); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, nil, err
	}
	ln.SetUnlinkOnClose(false)
	owned, err := os.Lstat(path)
	cleanup := func() {
		ln.Close()
		if owned != nil {
			_ = removeOwnedSocket(path, owned)
		}
	}
	if err == nil && groupName != "" {
		err = os.Chown(path, -1, gid)
	}
	if err == nil {
		err = os.Chmod(path, mode)
	}
	if err == nil {
		_, err = checkedSocket(path)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return ln, cleanup, nil
}

func removeOwnedSocket(path string, owned os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// BSD can immediately reuse an unlinked socket inode. Creation mtime is
	// part of this ownership snapshot; inode equality alone deleted a new
	// listener in the native OpenBSD restart test.
	if !os.SameFile(owned, current) || !owned.ModTime().Equal(current.ModTime()) {
		return errors.New("backchannel socket was replaced")
	}
	// Some filesystems reuse both inode and coarse timestamps. A replacement
	// that listens is never stale, even if its metadata matches the old socket.
	conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return errors.New("backchannel socket is listening; refusing removal")
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("cannot safely remove backchannel socket: %w", dialErr)
	}
	return os.Remove(path)
}

// NewUnixDialer verifies protected ancestry before unveil hides the parent
// directories. Each connection still checks the leaf type, owner and access.
// Only the trusted endpoint owner/root can subsequently alter these parents.
// A missing leaf is allowed at setup, so the authority/tunnel may start later.
func NewUnixDialer(path string) (func(context.Context) (net.Conn, error), error) {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	st, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("cannot verify backchannel directory owner")
	}
	uid, gid := st.Uid, st.Gid
	if err := socketDirectory(path, uid, gid); err != nil {
		return nil, err
	}
	return func(ctx context.Context) (net.Conn, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0007 != 0 ||
			(uid != 0 && st.Uid != uid) || (info.Mode().Perm()&0060 != 0 && st.Gid != gid) {
			return nil, errors.New("backchannel socket ownership or permissions differ from its protected directory")
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", path)
	}, nil
}

// DialUnix is the non-sandboxed convenience path used by transport probes.
func DialUnix(ctx context.Context, path string) (net.Conn, error) {
	dial, err := NewUnixDialer(path)
	if err != nil {
		return nil, err
	}
	return dial(ctx)
}
