package repoworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Lock guards reject the operation's force bit, not an observed lock token.
// Ordinary SVN token checks remain authoritative. No stdout: pre-lock stdout
// would override the lock token. Runs as a mode of the existing Go worker,
// without a shell, config reads, network, or a second executable dependency.
const LockGuardVersion = "filees.lock-force-guard/v1"

func RunLockGuard(args []string, stderr io.Writer) int {
	if len(args) == 5 && args[4] == "0" {
		return 0
	}
	fmt.Fprintln(stderr, "FileES: forced lock replacement/release is forbidden; use the authorized reservation flow.")
	return 1
}

func CheckLockGuardExecutable(executable string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("lock guard executable must be absolute")
	}
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("lock guard target must be an executable file")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, executable, "--lock-guard-version").Output()
	if err != nil || string(out) != LockGuardVersion+"\n" {
		return errors.New("worker does not support the lock guard protocol")
	}
	return nil
}

type LockGuardStatus struct {
	Hook  string `json:"hook"`
	State string `json:"state"` // installed link, missing, foreign; apply also verifies worker protocol
}

func lockGuardDirectory(repository string) (string, error) {
	if !filepath.IsAbs(repository) || !validRepo(repository) {
		return "", errors.New("lock guards require an absolute SVN repository")
	}
	hooks := filepath.Join(repository, "hooks")
	for _, p := range []string{repository, hooks} {
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("lock guard repository and hooks must be real directories")
		}
	}
	return hooks, nil
}

func InspectLockGuards(repository, executable string) ([]LockGuardStatus, error) {
	if !filepath.IsAbs(executable) {
		return nil, errors.New("lock guard executable must be absolute")
	}
	hooks, err := lockGuardDirectory(repository)
	if err != nil {
		return nil, err
	}
	var result []LockGuardStatus
	for _, name := range []string{"pre-lock", "pre-unlock"} {
		p := filepath.Join(hooks, name)
		row := LockGuardStatus{Hook: name, State: "foreign"}
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			row.State = "missing"
		} else if err != nil {
			return nil, err
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return nil, err
			}
			if target == executable {
				row.State = "installed"
			}
		}
		result = append(result, row)
	}
	return result, nil
}

// InstallLockGuards never overwrites or chains an operator's existing hook.
// Caller holds the worker/service-WC locks. A private lock serializes this
// installation too; atomic no-replace links protect against outside writers.
// A crash between hooks is explicitly visible and safely resumable; no claim
// of an atomic two-hook policy switch is made for an existing live repository.
func InstallLockGuards(repository, executable string) error {
	if err := CheckLockGuardExecutable(executable); err != nil {
		return err
	}
	hooks, err := lockGuardDirectory(repository)
	if err != nil {
		return err
	}
	return WithFileLock(filepath.Join(hooks, ".filees-lock-guards.lock"), func() error {
		states, err := InspectLockGuards(repository, executable)
		if err != nil {
			return err
		}
		for _, state := range states {
			if state.State == "foreign" {
				return fmt.Errorf("preserving existing %s: explicit hook integration required", state.Hook)
			}
		}
		for _, state := range states {
			if state.State == "installed" {
				continue
			}
			// Symlink creation is atomic and fails if ANY target already exists.
			if err := os.Symlink(executable, filepath.Join(hooks, state.Hook)); err != nil {
				return err
			}
		}
		return syncDirectory(hooks)
	})
}
