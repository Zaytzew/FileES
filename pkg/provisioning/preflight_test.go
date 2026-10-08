package provisioning

import (
	"errors"
	"filees/pkg/errcat"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreflightLocalPathRejectsFilesystemRootBeforeScanning(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	// Create performs no recursive scan in the old implementation either.
	// Never reproduce the bug by walking a real disk in an attach test.
	_, err := PreflightLocalPath(root, LocalPathCreate, nil)
	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Key != "repo.local_volume_root" {
		t.Fatalf("expected readable root refusal before disk inspection, got %v", err)
	}
}

func TestPreflightLocalPathRejectsAliasOfFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	alias := filepath.Join(t.TempDir(), "disk-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := PreflightLocalPath(alias, LocalPathCreate, nil)
	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Key != "repo.local_volume_root" {
		t.Fatalf("filesystem root via alias not refused: %v", err)
	}
}

func TestPreflightChildOfNonemptyParentRemainsAvailable(t *testing.T) {
	parent := t.TempDir()
	sibling := filepath.Join(parent, "existing-point")
	if err := os.MkdirAll(filepath.Join(sibling, ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, "$RECYCLE.BIN"), 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "new-point")
	check, err := PreflightLocalPath(child, LocalPathAttach, []string{sibling})
	if err != nil || check.CanonicalPath != child || check.Exists || !check.Empty {
		t.Fatalf("new child rejected or parent confused with target: %+v %v", check, err)
	}
}

func TestPreflightLocalPathCreateAcceptsExistingContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := PreflightLocalPath(root, LocalPathCreate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Exists || check.Empty || check.CanonicalPath != root {
		t.Fatalf("unexpected check: %#v", check)
	}
}

func TestPreflightLocalPathAttachRequiresEmptyTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "document.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightLocalPath(root, LocalPathAttach, nil); err == nil || !strings.Contains(err.Error(), "absent or empty") {
		t.Fatalf("expected non-empty attach rejection, got %v", err)
	}
	if err := os.Remove(filepath.Join(root, "document.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightLocalPath(root, LocalPathAttach, nil); err != nil {
		t.Fatalf("empty attach target rejected: %v", err)
	}
}

func TestPreflightLocalPathAttachResumeAcceptsOnlyExactWorkingCopyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "share")
	if err := os.MkdirAll(filepath.Join(root, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightLocalPath(root, LocalPathAttachResume, nil); err != nil {
		t.Fatalf("exact resumed WC rejected: %v", err)
	}
	if _, err := PreflightLocalPath(filepath.Join(root, "child"), LocalPathAttachResume, nil); err == nil {
		t.Fatal("path below an existing WC accepted as resumed root")
	}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightLocalPath(plain, LocalPathAttachResume, nil); err == nil {
		t.Fatal("plain directory accepted as resumed WC")
	}
}

func TestPreflightLocalPathRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"relative": "relative", "root": string(filepath.Separator), "file": file} {
		t.Run(name, func(t *testing.T) {
			if _, err := PreflightLocalPath(path, LocalPathCreate, nil); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if runtime.GOOS != "windows" {
		fifo := filepath.Join(root, "fifo")
		if err := os.Mkdir(fifo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(fifo); err != nil {
			t.Fatal(err)
		}
		// A directory is replaced with a symlink to a regular file to exercise
		// the non-directory check without opening a FIFO during the test.
		if err := os.Symlink(file, fifo); err != nil {
			t.Fatal(err)
		}
		if _, err := PreflightLocalPath(fifo, LocalPathCreate, nil); err == nil {
			t.Fatal("expected symlink-to-file rejection")
		}
	}
}

func TestPreflightLocalPathRejectsWorkingCopyAndOverlaps(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.MkdirAll(filepath.Join(existing, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightLocalPath(filepath.Join(existing, "child"), LocalPathCreate, nil); err == nil || !strings.Contains(err.Error(), "working copy") {
		t.Fatalf("expected working-copy rejection, got %v", err)
	}

	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{plain, filepath.Join(plain, "child"), root} {
		if _, err := PreflightLocalPath(candidate, LocalPathCreate, []string{plain}); err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("expected overlap rejection for %q, got %v", candidate, err)
		}
	}
}

func TestPreflightLocalPathResolvesMissingPathBelowSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	check, err := PreflightLocalPath(filepath.Join(alias, "missing"), LocalPathCreate, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(real, "missing")
	if check.CanonicalPath != want || check.Exists || !check.Empty {
		t.Fatalf("check = %#v, want canonical %q absent and empty", check, want)
	}
	if _, err := PreflightLocalPath(filepath.Join(alias, "missing"), LocalPathCreate, []string{real}); err == nil {
		t.Fatal("expected canonical symlink overlap rejection")
	}
}
