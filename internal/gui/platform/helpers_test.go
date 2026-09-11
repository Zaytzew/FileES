package platform

import (
	"path/filepath"
	"runtime"
	"testing"
)

// testRoot builds an absolute fixture root for the host platform. These tests
// used to hard-code filepath.Join(filepath.Separator, ...), which yields
// "\wc\repo" on Windows -- a rooted but *drive-relative* path that
// filepath.IsAbs correctly rejects, so every path fixture was refused before
// the assertion under test was ever reached. On anything but Windows this is
// exactly the previous value, so the fixtures are unchanged there.
func testRoot(elem ...string) string {
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, elem...)...)
}

func TestValidatePickedPaths(t *testing.T) {
	root := testRoot("wc", "repo")
	first := filepath.Join(root, "sub", "..", "a.dwg")
	second := filepath.Join(root, "b.dwg")

	got, err := ValidatePickedPaths(root, []string{first, first, second})
	if err != nil {
		t.Fatalf("ValidatePickedPaths: %v", err)
	}
	want := []string{filepath.Clean(first), second}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestValidatePickedPathsRejectsOutsideRoot(t *testing.T) {
	root := testRoot("wc", "repo")
	outside := testRoot("wc", "repo-other", "a.dwg")
	if _, err := ValidatePickedPaths(root, []string{outside}); err == nil {
		t.Fatal("expected outside-root path to be rejected")
	}
}

func TestValidatePickedPathsRejectsRelativeRootOrPath(t *testing.T) {
	absRoot := testRoot("wc", "repo")
	if _, err := ValidatePickedPaths("relative", []string{filepath.Join(absRoot, "a")}); err == nil {
		t.Fatal("expected relative root to be rejected")
	}
	if _, err := ValidatePickedPaths(absRoot, []string{"relative"}); err == nil {
		t.Fatal("expected relative path to be rejected")
	}
}
