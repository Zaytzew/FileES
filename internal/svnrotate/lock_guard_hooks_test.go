package svnrotate

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLockGuardEntrypointsSurviveGenerationReplacement(t *testing.T) {
	requireSVNTools(t)
	for _, action := range []string{"rotate", "load"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			repo := buildTestRepo(t, root, "before\n")
			// The hook binary is installation-owned and outside the FSFS tree.
			// A dangling fixture target verifies copying the LINK, not its bytes.
			target := filepath.Join(root, "installation", "filees-worker")
			for _, name := range []string{"pre-lock", "pre-unlock"} {
				if err := os.Symlink(target, filepath.Join(repo, "hooks", name)); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(root, "archive")
			if action == "rotate" {
				if err := Rotate(testConfig(repo, archive), "guard test", io.Discard); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := LoadGeneration(testLoadConfig(repo, archive), bytes.NewReader(testDump("after\n")), "guard test", io.Discard); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"pre-lock", "pre-unlock"} {
				if got, err := os.Readlink(filepath.Join(repo, "hooks", name)); err != nil || got != target {
					t.Fatalf("lost %s: %q %v", name, got, err)
				}
			}
		})
	}
}
