package platform

import (
	"path/filepath"
	"testing"
)

func TestValidatePickedPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "wc", "repo")
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
	root := filepath.Join(string(filepath.Separator), "wc", "repo")
	outside := filepath.Join(string(filepath.Separator), "wc", "repo-other", "a.dwg")
	if _, err := ValidatePickedPaths(root, []string{outside}); err == nil {
		t.Fatal("expected outside-root path to be rejected")
	}
}

func TestValidatePickedPathsRejectsRelativeRootOrPath(t *testing.T) {
	absRoot := filepath.Join(string(filepath.Separator), "wc", "repo")
	if _, err := ValidatePickedPaths("relative", []string{filepath.Join(absRoot, "a")}); err == nil {
		t.Fatal("expected relative root to be rejected")
	}
	if _, err := ValidatePickedPaths(absRoot, []string{"relative"}); err == nil {
		t.Fatal("expected relative path to be rejected")
	}
}
