package commit

import (
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

// missing builds an entry the way svn status actually delivers one.
//
// StatusEntry.Path is relative to the working copy, because parseStatusXML
// relativises it. The first version of these tests used absolute paths, so they
// passed against a BlocksUpdate that relativised a second time and classified
// every real entry as unclassifiable - the fix shipped, changed nothing, and
// the owner's working copies went on deferring. A test has to be given the
// shape the code under test is really handed.
func missing(parts ...string) client.StatusEntry {
	return client.StatusEntry{Path: filepath.Join(parts...), Item: "missing"}
}

// The deadlock this exists to break, in one test.
//
// AutoCAD's lock files were committed before r778 ignored them. Once ignored,
// the scanner stops looking at them - including at their disappearance - so
// their removal is never published. svn keeps calling them missing, the guard
// keeps deferring, and two of the owner's working copies received nothing from
// the server for a day without a single error being shown.
func TestIgnoredLockFilesDoNotHoldTheUpdateLaneShut(t *testing.T) {
	wc := filepath.Join(t.TempDir(), "GWIAZDZISTA2")
	entries := []client.StatusEntry{
		missing("OPRACOWANIE-TEREN", "GWDZ_schody-zewnetrzne.dwl"),
		missing("OPRACOWANIE-TEREN", "GWDZ_schody-zewnetrzne.dwl2"),
	}
	if BlocksUpdate(wc, entries) {
		t.Error("an update is deferred over files FileES does not manage; nothing will ever publish their removal")
	}
}

// The guard still does its job. It exists so svn update cannot resurrect a
// deletion the owner has made and FileES has not published yet.
func TestARealPendingDeletionStillDefersTheUpdate(t *testing.T) {
	wc := filepath.Join(t.TempDir(), "GWIAZDZISTA2")
	entries := []client.StatusEntry{
		missing("OPRACOWANIE-TEREN", "GWDZ_schody-zewnetrzne.dwl"),
		missing("OPRACOWANIE-TEREN", "rzut-parteru.dwg"),
	}
	if !BlocksUpdate(wc, entries) {
		t.Error("a drawing the owner deleted was not treated as blocking; the update would bring it back under him")
	}
}

func TestNothingMissingNeverBlocks(t *testing.T) {
	wc := t.TempDir()
	entries := []client.StatusEntry{
		{Path: "rzut-parteru.dwg", Item: "modified"},
		{Path: "nowy.pdf", Item: "unversioned"},
	}
	if BlocksUpdate(wc, entries) {
		t.Error("a working copy with no removals was treated as blocking")
	}
}

// A path that cannot be placed inside the working copy is treated as blocking.
// Failing towards the older, stricter behaviour keeps anything unclassifiable
// on the safe side of a decision about the owner's files.
func TestAnUnrelatablePathIsTreatedAsBlocking(t *testing.T) {
	wc := filepath.Join(t.TempDir(), "wc")
	outside := filepath.Join(t.TempDir(), "gdzie-indziej", "plik.dwl")
	if !BlocksUpdate(wc, []client.StatusEntry{{Path: outside, Item: "missing"}}) {
		t.Error("a missing path outside the working copy was not treated as blocking")
	}
}
