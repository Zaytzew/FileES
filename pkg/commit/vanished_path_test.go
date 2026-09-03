package commit

import (
	"os"
	"path/filepath"
	"testing"
)

// One file that went away must cost that file, never the batch it travelled in.
//
// Measured on live work 2026-09-03: a released PDF was seen once at 14:53:22,
// removed, and the commit two minutes later failed the whole batch with
// E200009 "is not under version control". The path stayed in the cache and
// every retry failed identically, holding 32 MB of real drawings behind one
// file that no longer existed.
func TestAVanishedPathCostsOnlyItself(t *testing.T) {
	wc := t.TempDir()
	for _, name := range []string{"rysunek.dwg", "zestawienie.pdf"} {
		if err := os.WriteFile(filepath.Join(wc, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(wc, "01_WYDANIE"), 0o700); err != nil {
		t.Fatal(err)
	}

	kept := existingPaths(wc, []string{"rysunek.dwg", "01_WYDANIE/znikniety.pdf", "zestawienie.pdf", "01_WYDANIE"})
	if len(kept) != 3 {
		t.Fatalf("kept = %v", kept)
	}
	for _, wanted := range []string{"rysunek.dwg", "zestawienie.pdf"} {
		if !contains(kept, wanted) {
			t.Fatalf("%q is still on disk and must still be committed: %v", wanted, kept)
		}
	}
	// A directory that is still there belongs in the commit, which is why this
	// checks existence rather than regularity.
	if !contains(kept, "01_WYDANIE") {
		t.Fatalf("an existing directory must survive the filter: %v", kept)
	}
	if contains(kept, "01_WYDANIE/znikniety.pdf") {
		t.Fatalf("a path that is gone must not reach svn commit: %v", kept)
	}
}

func contains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
