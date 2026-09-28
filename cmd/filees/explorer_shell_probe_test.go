//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/cloudfiles"
	"golang.org/x/sys/windows/registry"
)

func shellRootsForPath(t *testing.T, path string) []string {
	t.Helper()
	var result []string
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		key, err := registry.OpenKey(hive, `Software\Microsoft\Windows\CurrentVersion\Explorer\SyncRootManager`, registry.READ)
		if err == registry.ErrNotExist {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		names, err := key.ReadSubKeyNames(-1)
		if err != nil {
			key.Close()
			t.Fatal(err)
		}
		for _, name := range names {
			child, err := registry.OpenKey(key, name+`\UserSyncRoots`, registry.READ)
			if err != nil {
				continue
			}
			values, err := child.ReadValueNames(-1)
			if err != nil {
				child.Close()
				key.Close()
				t.Fatal(err)
			}
			for _, value := range values {
				root, _, err := child.GetStringValue(value)
				if err == nil && strings.EqualFold(filepath.Clean(root), filepath.Clean(path)) {
					result = append(result, name)
				}
			}
			child.Close()
		}
		key.Close()
	}
	return result
}

func TestAnchorShellRegistrationAndDetachAreIndependent(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	icon, err := anchorFolderIconPath()
	if err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: helper}
	ctx := context.Background()
	parent := t.TempDir()
	roots := []string{filepath.Join(parent, "Project-A"), filepath.Join(parent, "Project-B")}
	for _, root := range roots {
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if out, err := exec.Command(helper, "unregister", "--root", root).CombinedOutput(); err != nil {
				t.Errorf("cleanup: %s %v", out, err)
			}
		})
		for _, args := range [][]string{
			{"register", "--root", root, "--identity", "test\x1frepo"},
			{"shell-register", "--root", root, "--identity", "test\x1frepo", "--icon", icon},
			{"shell-register", "--root", root, "--identity", "test\x1frepo", "--icon", icon},
		} {
			answer, err := m.call(ctx, "", args...)
			if err != nil || !answer.OK {
				t.Fatalf("%v: %+v %v", args, answer, err)
			}
		}
		if names := shellRootsForPath(t, root); len(names) != 1 {
			t.Fatalf("shell registrations: %v", names)
		}
		answer, err := m.call(ctx, "", "info", "--root", root)
		if err != nil || !answer.OK || !answer.Ours || !cloudfiles.IsSyncRoot(root) {
			t.Fatalf("native identity lost: %+v %v", answer, err)
		}
	}
	if names := shellRootsForPath(t, parent); len(names) != 0 {
		t.Fatalf("parent registered: %v", names)
	}
	// Neither a plain parent nor a directory below a registered root is a root
	// this helper may claim or unregister.
	subdir := filepath.Join(roots[0], "ordinary-subfolder")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{parent, subdir} {
		answer, err := m.call(ctx, "", "shell-register", "--root", path, "--identity", "test\x1frepo", "--icon", icon)
		if err != nil || answer.OK {
			t.Fatalf("must reject non-root %s: %+v %v", path, answer, err)
		}
	}
	if answer, err := m.call(ctx, "", "unregister", "--root", subdir); err != nil || answer.OK {
		t.Fatalf("must reject unregister below root: %+v %v", answer, err)
	}
	if !cloudfiles.IsSyncRoot(roots[0]) {
		t.Fatal("negative call removed containing root")
	}
	for _, root := range roots {
		answer, err := m.call(ctx, "f\t120\tunread.txt\tunread.txt\n", "placeholders", "--root", root)
		if err != nil || !answer.OK {
			t.Fatalf("placeholders: %+v %v", answer, err)
		}
		if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte("keep me"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i, root := range roots {
		for attempt := 0; attempt < 2; attempt++ {
			answer, err := m.call(ctx, "", "unregister", "--root", root)
			if err != nil || !answer.OK {
				t.Fatalf("unregister retry %d: %+v %v", attempt, answer, err)
			}
		}
		if names := shellRootsForPath(t, root); len(names) != 0 {
			t.Fatalf("orphan registration: %v", names)
		}
		if cloudfiles.IsSyncRoot(root) {
			t.Fatal("native registration remains")
		}
		if raw, err := os.ReadFile(filepath.Join(root, "kept.txt")); err != nil || string(raw) != "keep me" {
			t.Fatal("local content changed")
		}
		if _, err := os.Lstat(filepath.Join(root, "unread.txt")); !os.IsNotExist(err) {
			t.Fatal("unread placeholder remains")
		}
		if i == 0 && len(shellRootsForPath(t, roots[1])) != 1 {
			t.Fatal("sibling registration removed")
		}
	}
}
