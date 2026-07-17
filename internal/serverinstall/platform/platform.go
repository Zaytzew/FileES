// Package platform performs the operating-system-level tasks that
// filees-install needs beyond ordinary file copies: user management,
// sshd config, login class (OpenBSD), state directory ownership and
// secret generation.
//
// Build tags (openbsd, linux) select the concrete implementation.
// Callers use the New() constructor and the Backend interface.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
)

// Backend is implemented per OS. The manifest's own files (sshd fragment,
// login.conf fragment) are installed by the updater like any other file;
// the backend only performs the system-level follow-up steps.
type Backend interface {
	// EnsureUsers creates each account that does not already exist and
	// returns the names of those it actually created.
	EnsureUsers(names []string) (created []string, err error)

	// RegisterLoginConf runs the OS-specific step that activates an
	// already-installed login.conf fragment (cap_mkdb on OpenBSD; no-op
	// on Linux).
	RegisterLoginConf(path string) error

	// EnsureSSHDInclude checks that sshdConfig includes sshdConfDir and,
	// if not, backs sshdConfig up and appends the Include directive.
	// Returns the backup path or "" when already present.
	EnsureSSHDInclude(sshdConfDir, sshdConfig string) (backup string, err error)

	// ApplyStateDirs creates all required /var/filees and /etc/filees
	// subdirectories and sets ownership / permissions using stateOwner.
	ApplyStateDirs(stateOwner string) error

	// GenerateSecrets creates otp.pepper and worker_ed25519 under confDir
	// if they do not already exist.
	GenerateSecrets(confDir string) error

	// ReloadSSHD validates the running config with sshd -t and then
	// reloads the daemon.
	ReloadSSHD() error

	// RemoveUser deletes a user if it still exists; a no-op if not found.
	RemoveUser(name string) error

	// RemoveLoginConf removes the installed login.conf fragment and runs
	// the post-remove step (cap_mkdb on OpenBSD; no-op on Linux).
	RemoveLoginConf(path string) error
}

// lookupUID returns the numeric UID/GID of the named user account.
func lookupUID(name string) (uid, gid int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid64, err := strconv.ParseInt(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("bad uid %q for %s", u.Uid, name)
	}
	gid64, err := strconv.ParseInt(u.Gid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("bad gid %q for %s", u.Gid, name)
	}
	return int(uid64), int(gid64), nil
}

// userExists returns true if name is found in the OS user database.
func userExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}

// resolve returns the absolute path of program, searching PATH if needed.
// Must be called before any unveil() so PATH-scanning can see the real FS.
func resolve(program string) (string, error) {
	p, err := exec.LookPath(program)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", program, err)
	}
	return p, nil
}

// mkdirChown creates dir at mode and sets its owner.
func mkdirChown(dir string, mode os.FileMode, uid, gid int) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	if err := os.Chmod(dir, mode); err != nil {
		return err
	}
	return os.Lchown(dir, uid, gid)
}
