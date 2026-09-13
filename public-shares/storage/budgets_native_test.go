//go:build linux || openbsd

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBudgetsNativeProbeResolvesAliasesAndMissingDirectories(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	volumes, err := InspectBudgets([]Budget{{filepath.Join(alias, "missing"), 1}, {filepath.Join(target, "other"), 2}})
	if err != nil || len(volumes) != 1 || volumes[0].Required != 3 {
		t.Fatalf("%+v %v", volumes, err)
	}
	if volumes[0].Device == "" || volumes[0].Available < 0 {
		t.Fatalf("%+v", volumes)
	}
	for _, p := range volumes[0].Paths {
		if filepath.Dir(p) != target {
			t.Fatalf("alias not resolved: %s", p)
		}
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBudgets([]Budget{{alias, 1}}); err == nil {
		t.Fatal("dangling symlink accepted")
	}
}
