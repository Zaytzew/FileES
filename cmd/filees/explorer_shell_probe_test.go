//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filees/pkg/cloudfiles"
	"filees/pkg/localrepo"
	"github.com/google/uuid"
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
		} else if shellRootPinned(t, names[0]) != 0 || shellRootEnumerated(t, names[0]) {
			t.Fatal("registration implicitly pinned navigation shortcut")
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

func TestAnchorShellHideObsoletePreservesMovedNativeRoot(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI")
	icon, err := anchorFolderIconPath()
	if err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: helper}
	ctx := t.Context()
	parent := t.TempDir()
	old := filepath.Join(parent, "Old-Zażółć-東京")
	moved := filepath.Join(parent, "Moved-Zażółć-東京")
	sibling := filepath.Join(parent, "Active-sibling")
	for _, root := range []string{old, sibling} {
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"register", "--root", root, "--identity", "synthetic-shell"}, {"shell-register", "--root", root, "--identity", "synthetic-shell", "--icon", icon, "--visible", "1"}} {
			answer, err := m.call(ctx, "", args...)
			if err != nil || !answer.OK {
				t.Fatal(args, answer, err)
			}
		}
	}
	t.Cleanup(func() {
		for _, root := range []string{old, moved, sibling} {
			out, err := exec.Command(helper, "unregister", "--root", root).CombinedOutput()
			if err != nil {
				t.Errorf("fixture cleanup %s: %s %v", root, out, err)
			}
		}
	})
	ids := shellRootsForPath(t, old)
	if len(ids) != 1 {
		t.Fatal("fixture root", ids)
	}
	if err := os.WriteFile(filepath.Join(old, "kept.txt"), []byte("retained bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "1", "0", "1"} {
		answer, err := m.call(ctx, "", "shell-visibility", "--root", old, "--visible", value)
		if err != nil || !answer.OK || !cloudfiles.IsSyncRoot(old) || shellRootPinned(t, ids[0]) != uint64(value[0]-'0') || shellRootEnumerated(t, ids[0]) != (value == "1") {
			t.Fatal("visibility changed CFAPI or did not apply", answer, err)
		}
	}
	// A label is not enough to own a navigation link. Refuse foreign/default
	// metadata, extra values and child keys before changing its pin or deleting.
	link, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\Desktop\NameSpace\`+shellRootNamespace(t, ids[0]), registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{"foreign-id", "extra-value", "child"} {
		switch malformed {
		case "foreign-id":
			err = link.SetStringValue("", "another-provider")
		case "extra-value":
			err = link.SetDWordValue("Unrelated", 42)
		case "child":
			var child registry.Key
			child, _, err = registry.CreateKey(link, "Unrelated", registry.ALL_ACCESS)
			child.Close()
		}
		if err != nil {
			link.Close()
			t.Fatal(err)
		}
		answer, callErr := m.call(ctx, "", "shell-visibility", "--root", old, "--visible", "0")
		if callErr != nil || answer.OK || shellRootPinned(t, ids[0]) != 1 || !cloudfiles.IsSyncRoot(old) {
			link.Close()
			t.Fatal("unknown navigation metadata changed", malformed, answer, callErr)
		}
		switch malformed {
		case "foreign-id":
			err = link.SetStringValue("", ids[0])
		case "extra-value":
			err = link.DeleteValue("Unrelated")
		case "child":
			err = registry.DeleteKey(link, "Unrelated")
		}
		if err != nil {
			link.Close()
			t.Fatal("restore disposable navigation link", err)
		}
		if !shellRootEnumerated(t, ids[0]) {
			link.Close()
			t.Fatal("unknown link removed")
		}
	}
	link.Close()
	if answer, err := m.call(ctx, "", "shell-visibility", "--root", parent, "--visible", "1"); err != nil || answer.OK || !cloudfiles.IsSyncRoot(old) {
		t.Fatal("ordinary folder visibility accepted", answer, err)
	}
	answer, err := m.call(ctx, "", "shell-hide-obsolete", "--root", old, "--registration-id", ids[0])
	if err != nil || answer.OK {
		t.Fatal("existing root must be refused", answer, err)
	}
	if err := os.Rename(old, moved); err != nil {
		t.Fatal("move unconnected fixture", err)
	}
	if !cloudfiles.IsSyncRoot(moved) {
		t.Fatal("moved native root was removed")
	}
	if len(shellRootsForPath(t, old)) != 1 {
		t.Fatal("fixture did not retain old Shell entry")
	}
	wrong := ids[0] + "x"
	answer, err = m.call(ctx, "", "shell-hide-obsolete", "--root", old, "--registration-id", wrong)
	if err != nil || answer.OK || len(shellRootsForPath(t, old)) != 1 {
		t.Fatal("wrong ID changed registration", answer, err)
	}
	// Refuse an unrelated namespace target even when the outer ID/path match.
	properties, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\CLSID\`+shellRootNamespace(t, ids[0])+`\Instance\InitPropertyBag`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := properties.GetStringValue("TargetFolderPath")
	if err != nil {
		properties.Close()
		t.Fatal(err)
	}
	if err := properties.SetStringValue("TargetFolderPath", sibling); err != nil {
		properties.Close()
		t.Fatal(err)
	}
	answer, callErr := m.call(ctx, "", "shell-hide-obsolete", "--root", old, "--registration-id", ids[0])
	restoreErr := properties.SetStringValue("TargetFolderPath", original)
	properties.Close()
	if restoreErr != nil {
		t.Fatal("restore fixture binding", restoreErr)
	}
	if callErr != nil || answer.OK || shellRootPinned(t, ids[0]) != 1 || !cloudfiles.IsSyncRoot(moved) || !cloudfiles.IsSyncRoot(sibling) {
		t.Fatal("wrong namespace target changed either root", answer, callErr)
	}
	// A previous registration can leave another Desktop link for the same ID
	// that is no longer referenced by SyncRootManager.NamespaceCLSID.
	duplicate := "{" + uuid.NewString() + "}"
	duplicateClass := `Software\Classes\CLSID\` + duplicate
	duplicateLink := `Software\Microsoft\Windows\CurrentVersion\Explorer\Desktop\NameSpace\` + duplicate
	for _, fixture := range []struct{ path, name, value string }{
		{duplicateClass, "", "FileES — retired fixture"},
		{duplicateClass + `\Instance\InitPropertyBag`, "TargetFolderPath", old},
		{duplicateLink, "", ids[0]},
	} {
		key, _, err := registry.CreateKey(registry.CURRENT_USER, fixture.path, registry.ALL_ACCESS)
		if err != nil {
			t.Fatal(err)
		}
		err = key.SetStringValue(fixture.name, fixture.value)
		key.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, duplicateClass+`\ShellFolder`, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	err = key.SetDWordValue("Attributes", 0xF080004D)
	key.Close()
	if err != nil {
		t.Fatal(err)
	}
	key, err = registry.OpenKey(registry.CURRENT_USER, duplicateClass, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	err = key.SetDWordValue("System.IsPinnedToNameSpaceTree", 1)
	key.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, path := range []string{duplicateLink, duplicateClass + `\Instance\InitPropertyBag`, duplicateClass + `\Instance`, duplicateClass + `\ShellFolder`, duplicateClass} {
			if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil && err != registry.ErrNotExist {
				t.Error("remove exact disposable namespace key", err)
			}
		}
	})
	for range 2 {
		answer, err = m.call(ctx, "", "shell-hide-obsolete", "--root", old, "--registration-id", ids[0])
		if err != nil || !answer.OK {
			t.Fatal("hide obsolete shortcut/retry", answer, err)
		}
		if len(shellRootsForPath(t, old)) != 1 || shellRootPinned(t, ids[0]) != 0 || shellRootEnumerated(t, ids[0]) || !cloudfiles.IsSyncRoot(moved) {
			t.Fatal("shortcut not hidden or native registration changed")
		}
		key, err := registry.OpenKey(registry.CURRENT_USER, duplicateLink, registry.READ)
		if err == nil {
			key.Close()
			t.Fatal("unreferenced duplicate navigation link remains")
		}
		if err != registry.ErrNotExist {
			t.Fatal(err)
		}
		key, err = registry.OpenKey(registry.CURRENT_USER, duplicateClass+`\ShellFolder`, registry.READ)
		if err != nil {
			t.Fatal(err)
		}
		flags, _, err := key.GetIntegerValue("Attributes")
		key.Close()
		if err != nil || flags != 0xF090004D {
			t.Fatal("duplicate class changed unrelated flags or remains enumerable", flags, err)
		}
	}
	// Exercise the daemon's sweep with discovery explicitly limited to this
	// fixture. Never feed production registrations to a test manager.
	store, err := localrepo.Open(filepath.Join(parent, "fixture-lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.lifecycle, m.localIntentMu = store, new(sync.Mutex)
	calls := 0
	discover := func(string) ([]anchorShellRoot, error) {
		calls++
		return []anchorShellRoot{{id: ids[0], path: old}}, nil
	}
	if !m.reconcileShellWith(ctx, discover) || !m.reconcileShellWith(ctx, discover) || calls != 1 {
		t.Fatal("sweep is not bounded to lifecycle changes", calls)
	}
	if shellRootPinned(t, ids[0]) != 0 || !cloudfiles.IsSyncRoot(moved) {
		t.Fatal("daemon sweep changed the native point")
	}
	if raw, err := os.ReadFile(filepath.Join(moved, "kept.txt")); err != nil || string(raw) != "retained bytes" {
		t.Fatal("bytes changed", err)
	}
	siblings := shellRootsForPath(t, sibling)
	if len(siblings) != 1 || !cloudfiles.IsSyncRoot(sibling) || shellRootPinned(t, siblings[0]) != 1 {
		t.Fatal("active sibling changed")
	}
	// Native daemon preference roundtrip on this fixture only, no owner roots.
	r, err := store.BeginAnchorAttach("test", "sibling", sibling, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApproveAttach(r.OperationID, "test", "sibling", "svn+ssh://example/sibling", "rw"); err != nil {
		t.Fatal(err)
	}
	r, err = store.MarkAttached(r.OperationID, "sibling")
	if err != nil {
		t.Fatal(err)
	}
	m.options = &anchorOptions{}
	m.options.servers.Store(map[string]bool{"test": true})
	if err = m.registerShellPreference(ctx, r, icon); err != nil || shellRootPinned(t, siblings[0]) != 0 {
		t.Fatal("implicit shortcut or registration failed", err)
	}
	for _, visible := range []bool{true, false, true} {
		state, err := m.ExplorerShortcut(ctx, "test", "sibling", &visible, r.OperationID)
		if err != nil || state.Visible != visible || shellRootPinned(t, siblings[0]) != uint64(boolInt(visible)) || shellRootEnumerated(t, siblings[0]) != visible || !cloudfiles.IsSyncRoot(sibling) || !cloudfiles.IsSyncRoot(moved) {
			t.Fatal("scoped preference changed native roots", state, err)
		}
	}
	if _, err := m.ExplorerShortcut(ctx, "other", "sibling", nil, ""); err == nil {
		t.Fatal("unknown server accepted")
	}
	reopened, err := localrepo.Open(filepath.Join(parent, "fixture-lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.lifecycle = reopened
	if state, err := m.ExplorerShortcut(ctx, "test", "sibling", nil, ""); err != nil || !state.Visible {
		t.Fatal("restart lost preference", state, err)
	}
	no := false
	if _, err := m.ExplorerShortcut(ctx, "test", "sibling", &no, "previous-copy"); err == nil || shellRootPinned(t, siblings[0]) != 1 {
		t.Fatal("old confirmation changed another WC")
	}
}

func shellRootNamespace(t *testing.T, id string) string {
	t.Helper()
	var namespace string
	for _, hive := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		key, err := registry.OpenKey(hive, `Software\Microsoft\Windows\CurrentVersion\Explorer\SyncRootManager\`+id, registry.READ)
		if err == registry.ErrNotExist {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		namespace, _, err = key.GetStringValue("NamespaceCLSID")
		key.Close()
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if namespace == "" {
		t.Fatal("missing fixture namespace")
	}
	return namespace
}

func shellRootPinned(t *testing.T, id string) uint64 {
	t.Helper()
	namespace := shellRootNamespace(t, id)
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\CLSID\`+namespace, registry.READ)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue("System.IsPinnedToNameSpaceTree")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// Desktop enumeration, unlike the pin, also controls "Show all folders".
func shellRootEnumerated(t *testing.T, id string) bool {
	t.Helper()
	namespace := shellRootNamespace(t, id)
	attributes, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\CLSID\`+namespace+`\ShellFolder`, registry.READ)
	if err != nil {
		t.Fatal(err)
	}
	flags, _, err := attributes.GetIntegerValue("Attributes")
	attributes.Close()
	if err != nil {
		t.Fatal(err)
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\Desktop\NameSpace\`+namespace, registry.READ)
	if err == registry.ErrNotExist {
		if flags&0x00100000 == 0 {
			t.Fatal("hidden namespace class remains enumerable")
		}
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	value, _, err := key.GetStringValue("")
	if err != nil || value != id || flags&0x00100000 != 0 {
		t.Fatal("unexpected Desktop namespace binding", value, err)
	}
	return true
}
