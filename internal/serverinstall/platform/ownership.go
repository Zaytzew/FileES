package platform

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// Ownership is the numeric identity committed to the filesystem. Manifests
// use names for reviewability; install history stores these resolved IDs so a
// rollback restores the exact pre-upgrade inode ownership even if account
// databases later change.
type Ownership struct {
	UID int
	GID int
}

// OwnershipManager separates identity lookup and inode metadata operations
// from the updater. The real implementation is used in production; tests use
// a deterministic fake and do not need privileged local accounts.
type OwnershipManager interface {
	Resolve(owner, group string) (Ownership, error)
	Stat(path string) (Ownership, error)
	Apply(path string, ownership Ownership) error
}

type SystemOwnership struct{}

func (SystemOwnership) Resolve(owner, group string) (Ownership, error) {
	u, err := user.Lookup(owner)
	if err != nil {
		return Ownership{}, fmt.Errorf("lookup owner %q: %w", owner, err)
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return Ownership{}, fmt.Errorf("lookup group %q: %w", group, err)
	}
	uid, err := parseIdentityID("uid", owner, u.Uid)
	if err != nil {
		return Ownership{}, err
	}
	gid, err := parseIdentityID("gid", group, g.Gid)
	if err != nil {
		return Ownership{}, err
	}
	return Ownership{UID: uid, GID: gid}, nil
}

func (SystemOwnership) Stat(path string) (Ownership, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Ownership{}, err
	}
	return statOwnership(info)
}

func (SystemOwnership) Apply(path string, ownership Ownership) error {
	return os.Lchown(path, ownership.UID, ownership.GID)
}

func parseIdentityID(kind, name, value string) (int, error) {
	id, err := strconv.ParseInt(value, 10, 32)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("bad %s %q for %s", kind, value, name)
	}
	return int(id), nil
}
