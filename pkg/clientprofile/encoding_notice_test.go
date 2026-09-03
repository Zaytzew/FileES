package clientprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An encoded directory name reads like a fault to whoever meets it next, and a
// plausible wrong explanation is worse than none: the reader stops looking.
// Two names cost hours on 2026-09-03 exactly that way - a mail state called
// "queued" that meant delivered, and a config error naming the wrong cause. The
// rule taken from it is that a name reading as a defect needs a sentence where
// it is read, and this one is read in a file manager.
func TestAnEncodedNameLeavesItsExplanationOnDisk(t *testing.T) {
	root := t.TempDir()
	if _, err := ServerDir(root, "atmprojekt:filees"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, encodingNoticeName))
	if err != nil {
		t.Fatalf("nothing explains the encoded name where someone will find it: %v", err)
	}
	notice := string(raw)
	for _, wanted := range []string{"atmprojekt+3Afilees", "atmprojekt:filees", "nie jest usterka", "unknown key %3"} {
		if !strings.Contains(notice, wanted) {
			t.Fatalf("the notice omits %q: %s", wanted, notice)
		}
	}
}

// A server whose ID the filesystem accepts is stored under that ID, so there is
// nothing to explain and nothing should appear. Writing the notice regardless
// would make it furniture, and furniture is not read.
func TestNothingIsExplainedWhenNothingWasEncoded(t *testing.T) {
	root := t.TempDir()
	dir, err := ServerDir(root, "spot")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != "spot" {
		t.Fatalf("an ordinary identifier must stay literal: %s", dir)
	}
	if _, err := os.Stat(filepath.Join(root, encodingNoticeName)); !os.IsNotExist(err) {
		t.Fatalf("nothing was encoded, so nothing should be explained: %v", err)
	}
}

// Resolving a profile directory must never fail because the explanation could
// not be written beside it.
func TestAnUnwritableRootStillResolves(t *testing.T) {
	dir, err := ServerDir(filepath.Join(t.TempDir(), "nie", "istnieje", "jeszcze"), "atmprojekt:filees")
	if err != nil {
		t.Fatalf("path resolution must not depend on the notice: %v", err)
	}
	if filepath.Base(dir) != "atmprojekt+3Afilees" {
		t.Fatalf("dir = %s", dir)
	}
}
