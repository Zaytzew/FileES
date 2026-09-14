//go:build openbsd

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"golang.org/x/sys/unix"
)

const adminStateUser = "_filees-state"

// prepareAdminIdentity prevents an administrator's root shell from creating
// root-owned files inside the private state tree. Network-facing entry points
// already cross a narrow set-id boundary before touching this data, while the
// operator-facing filees-admin historically relied on every human remembering
// `doas -u _filees-state`. One forgotten prefix produced a valid invitation
// whose later bootstrap could not read its root-owned ticket.
//
// A non-root invocation is unchanged and must already have access. Root drops
// all three UID/GID values and replaces supplementary groups before any config
// or state file is opened. The transition is therefore one-way; filees-admin
// cannot regain root later in the command.
func prepareAdminIdentity() error {
	if os.Geteuid() != 0 {
		return nil
	}

	target, err := user.Lookup(adminStateUser)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", adminStateUser, err)
	}
	uid, err := strconv.Atoi(target.Uid)
	if err != nil {
		return fmt.Errorf("parse %s uid %q: %w", adminStateUser, target.Uid, err)
	}
	gid, err := strconv.Atoi(target.Gid)
	if err != nil {
		return fmt.Errorf("parse %s gid %q: %w", adminStateUser, target.Gid, err)
	}
	groupIDs, err := target.GroupIds()
	if err != nil {
		return fmt.Errorf("list %s groups: %w", adminStateUser, err)
	}
	groups := make([]int, 0, len(groupIDs)+1)
	seen := make(map[int]struct{}, len(groupIDs)+1)
	for _, raw := range append(groupIDs, target.Gid) {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return fmt.Errorf("parse %s group %q: %w", adminStateUser, raw, parseErr)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		groups = append(groups, value)
	}

	if err := unix.Setgroups(groups); err != nil {
		return fmt.Errorf("set supplementary groups: %w", err)
	}
	if err := unix.Setresgid(gid, gid, gid); err != nil {
		return fmt.Errorf("drop gid to %d: %w", gid, err)
	}
	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return fmt.Errorf("drop uid to %d: %w", uid, err)
	}
	if os.Getuid() != uid || os.Geteuid() != uid || os.Getgid() != gid || os.Getegid() != gid {
		return fmt.Errorf("identity verification failed: uid=%d euid=%d gid=%d egid=%d", os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid())
	}
	return nil
}
