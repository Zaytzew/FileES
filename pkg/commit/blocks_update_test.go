package commit

import (
	"path/filepath"
	"testing"

	"filees/pkg/client"
)

func missing(wc string, parts ...string) client.StatusEntry {
	return client.StatusEntry{Path: filepath.Join(append([]string{wc}, parts...)...), Item: "missing"}
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
		missing(wc, "OPRACOWANIE-TEREN", "GWDZ_schody-zewnetrzne.dwl"),
		missing(wc, "OPRACOWANIE-TEREN", "GWDZ_schody-zewnetrzne.dwl2"),
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
		missing(wc, "OPRACOWANIE-TEREN", "GWDZ_schody-zewnetrzne.dwl"),
		missing(wc, "OPRACOWANIE-TEREN", "rzut-parteru.dwg"),
	}
	if !BlocksUpdate(wc, entries) {
		t.Error("a drawing the owner deleted was not treated as blocking; the update would bring it back under him")
	}
}

func TestNothingMissingNeverBlocks(t *testing.T) {
	wc := t.TempDir()
	entries := []client.StatusEntry{
		{Path: filepath.Join(wc, "rzut-parteru.dwg"), Item: "modified"},
		{Path: filepath.Join(wc, "nowy.pdf"), Item: "unversioned"},
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
