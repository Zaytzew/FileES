//go:build !windows

package repoworker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCorrectServiceWorkingCopyOwnershipIsBoundedAndIdempotent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("ownership correction requires a root test process")
	}
	root := t.TempDir()
	activationRoot := filepath.Join(root, "activation")
	workingCopy := filepath.Join(root, "service-wc")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{activationRoot, filepath.Join(workingCopy, ".svn", "pristine", "aa")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workingCopy, ".svn", "pristine", "aa", "object.svn-base"), []byte("canonical\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workingCopy, "outside-link")); err != nil {
		t.Fatal(err)
	}
	const targetUID, targetGID = 1, 1
	if err := os.Chown(activationRoot, targetUID, targetGID); err != nil {
		t.Fatal(err)
	}

	result, err := CorrectServiceWorkingCopyOwnership(activationRoot, workingCopy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Inspected < 5 || result.Corrected != result.Inspected {
		t.Fatalf("first correction = %+v", result)
	}
	for _, path := range []string{workingCopy, filepath.Join(workingCopy, ".svn", "pristine", "aa", "object.svn-base"), filepath.Join(workingCopy, "outside-link"), filepath.Join(activationRoot, ".service-wc.lock")} {
		stat := lstatOwnership(t, path)
		if int(stat.Uid) != targetUID || int(stat.Gid) != targetGID {
			t.Fatalf("%s owner=%d:%d", path, stat.Uid, stat.Gid)
		}
	}
	if stat := lstatOwnership(t, outside); stat.Uid != 0 {
		t.Fatalf("corrector followed symlink outside service WC: uid=%d", stat.Uid)
	}
	if info, err := os.Stat(filepath.Join(workingCopy, ".svn", "pristine", "aa", "object.svn-base")); err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("corrector changed mode: info=%v err=%v", info, err)
	}

	second, err := CorrectServiceWorkingCopyOwnership(activationRoot, workingCopy)
	if err != nil || second.Corrected != 0 || second.Inspected != result.Inspected {
		t.Fatalf("second correction = %+v err=%v", second, err)
	}
}

func lstatOwnership(t *testing.T, path string) *syscall.Stat_t {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s has no syscall.Stat_t", path)
	}
	return stat
}
