//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Only synthetic folders are registered; a parent and a sibling must remain
// usable even when Windows returns provider metadata from an unrelated point.
func TestAnchorParentAndSiblingRemainAvailableNative(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI")
	parent := t.TempDir()
	point := filepath.Join(parent, "point-東京")
	sibling := filepath.Join(parent, "point-東京-other")
	inside := filepath.Join(point, "inside")
	for _, p := range []string{point, sibling, inside} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	m := &anchorManager{helper: helper}
	answer, err := m.call(t.Context(), "", "register", "--root", point, "--identity", "synthetic-parent-probe")
	if err != nil || !answer.OK {
		t.Fatalf("register: %+v %v", answer, err)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(helper, "unregister", "--root", point).CombinedOutput(); err != nil {
			t.Errorf("unregister synthetic point: %s (%v)", out, err)
		}
	})
	for _, p := range []string{parent, sibling, filepath.Join(parent, "missing-sibling")} {
		if err := m.precheck(p); err != nil {
			t.Errorf("unrelated folder %q refused: %v", p, err)
		}
	}
	for _, p := range []string{point, inside, filepath.Join(inside, "missing-child")} {
		if err := m.precheck(p); err == nil {
			t.Errorf("nested point %q accepted", p)
		}
	}
	// Following a directory alias must keep the actual provider boundary.
	for _, tc := range []struct {
		name, target string
		blocked      bool
	}{
		{"alias-inside", inside, true}, {"alias-outside", sibling, false},
	} {
		alias := filepath.Join(parent, tc.name)
		if err := os.Symlink(tc.target, alias); err != nil {
			t.Logf("directory alias unavailable: %v", err)
			continue
		}
		err := m.precheck(filepath.Join(alias, "new-child"))
		if (err != nil) != tc.blocked {
			t.Errorf("alias %s: blocked=%v, error=%v", tc.name, tc.blocked, err)
		}
	}
	// Optional read-only reproduction on the owner's reported parent. Never
	// register, unregister or enumerate children at this path.
	if p := os.Getenv("FILEES_CFAPI_PARENT_PROBE"); p != "" {
		if err := m.precheck(filepath.Join(p, "BONIFACIO-PROJEKT-BUDOWLANY")); err != nil {
			t.Errorf("reported parent still refused: %v", err)
		}
	}
}
