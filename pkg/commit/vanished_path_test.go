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

	kept := publishable(wc, []string{"rysunek.dwg", "01_WYDANIE/znikniety.pdf", "zestawienie.pdf", "01_WYDANIE"})
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

// Ignoring used to apply only when a file was first taken in, so a file already
// under version control kept being published on every change and the patterns
// did nothing for any working copy that already contained the clutter.
//
// Measured 2026-09-03: .dwl and .dwl2 were committed at r16 by this very client
// minutes after the patterns shipped, because AutoCAD had them open and SVN
// already tracked them.
func TestAlreadyVersionedClutterStopsBeingPublished(t *testing.T) {
	wc := t.TempDir()
	for _, name := range []string{
		"LLW_zestawienie-stolarki.dwg",
		"LLW_zestawienie-stolarki.dwl",
		"LLW_zestawienie-stolarki.dwl2",
	} {
		if err := os.WriteFile(filepath.Join(wc, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	kept := publishable(wc, []string{
		"LLW_zestawienie-stolarki.dwg",
		"LLW_zestawienie-stolarki.dwl",
		"LLW_zestawienie-stolarki.dwl2",
	})
	if len(kept) != 1 || kept[0] != "LLW_zestawienie-stolarki.dwg" {
		t.Fatalf("only the drawing may be published; got %v", kept)
	}
}

// A rename whose destination never arrived is not a rename. Word saves by
// writing a temporary file, deleting the original and renaming over it, which
// the watcher reasonably reads as a rename; if the document moves again before
// the commit runs, the destination is gone and svn commit fails the whole batch
// on a path it never knew.
//
// Measured 2026-09-03 at 17:18 on live work, minutes after the same shape was
// fixed for plain additions in r779 and left unfixed on this branch.
func TestARenameWithNoDestinationIsDropped(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "arrived.docx"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The source of a real rename is meant to be gone, so it can never be
	// checked for existence - only the destination can.
	if got := existingPaths(wc, []string{"arrived.docx"}); len(got) != 1 {
		t.Fatalf("a destination that arrived must be committed: %v", got)
	}
	if got := existingPaths(wc, []string{"01_EDITABLES/ST-00.01_Wymagania-ogolne.docx"}); len(got) != 0 {
		t.Fatalf("a destination that never arrived must not: %v", got)
	}
}
