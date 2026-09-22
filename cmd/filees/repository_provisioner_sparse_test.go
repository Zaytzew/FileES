package main

import (
	"path/filepath"
	"testing"
)

// The sparse attachment's own .filees must not read as a modified checkout,
// in whichever spelling svn reports it; anything else still does.
func TestSparseAttachmentIgnoresOnlyItsOwnControlDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "copy")
	for _, entry := range []string{".filees", ".filees/state", filepath.Join(root, ".filees"), filepath.Join(root, ".filees", "state", "working-copy.json")} {
		if !isFileESControlPath(root, entry) {
			t.Fatalf("%s not recognised as the control directory", entry)
		}
	}
	for _, entry := range []string{"art", ".fileesx", "docs/.filees", filepath.Join(root, "art", "model.blend")} {
		if isFileESControlPath(root, entry) {
			t.Fatalf("%s wrongly treated as the control directory", entry)
		}
	}
}
