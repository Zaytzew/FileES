package main

import (
	"path/filepath"
	"testing"

	"filees/pkg/localrepo"
)

// A repository the server no longer carries must still be identifiable.
//
// A12 in the seam register, taken from the owner's screen: a row reading
// 5362797d-fed3-5aff-b6bd-640702a8bc4b, offering no action, sitting exactly
// where he had to decide whether to download its archive. The folder was in the
// same record all along.
//
// The path is built natively rather than written as a Windows literal.
// filepath.Base is platform-dependent, so "D:\AKTUALNE" is one whole name on
// POSIX and this test would assert something the code never does there. That is
// exactly how the picker tests in this repository came to pass only on Windows,
// and the first draft of this one repeated it - caught on the OpenBSD VM.
func TestADeletedRepositoryFallsBackToItsFolderName(t *testing.T) {
	got := deletedRepositoryName(localrepo.Record{
		RepoID:    "5362797d-fed3-5aff-b6bd-640702a8bc4b",
		LocalPath: filepath.Join(t.TempDir(), "AKTUALNE"),
	})
	if got != "AKTUALNE" {
		t.Fatalf("name = %q; the folder is in the record and says what this was", got)
	}
}

func TestARecordedDisplayNameWinsOverTheFolder(t *testing.T) {
	got := deletedRepositoryName(localrepo.Record{
		RepoID:      "uid",
		DisplayName: "Willa Zachodnia",
		LocalPath:   filepath.Join(t.TempDir(), "willa-2"),
	})
	if got != "Willa Zachodnia" {
		t.Fatalf("name = %q; a name chosen deliberately outranks a folder name", got)
	}
}

// The UID stays as the last resort. A row that identifies nothing is still
// better than a row that names the wrong thing.
func TestWithoutAnyNameTheUIDRemains(t *testing.T) {
	if got := deletedRepositoryName(localrepo.Record{RepoID: "uid"}); got != "uid" {
		t.Fatalf("name = %q, want the repository ID", got)
	}
}

// Adoption of a folder already on disk records what to call it, so a later
// deletion does not have to reconstruct the name from a path that may itself be
// gone. BeginCreate always did this; BeginAttach did not, and BeginAttach is
// the path the owner's repository took.
func TestAttachingAFolderRecordsItsName(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "repository-lifecycle.json"))
	if err != nil {
		t.Fatalf("open lifecycle: %v", err)
	}
	local := filepath.Join(t.TempDir(), "KIWERSKA-SALA-GIMNASTYCZNA")
	record, err := store.BeginAttach("atmprojekt:filees", "repo-uid", local, false)
	if err != nil {
		t.Fatalf("BeginAttach: %v", err)
	}
	if record.DisplayName != "KIWERSKA-SALA-GIMNASTYCZNA" {
		t.Fatalf("DisplayName = %q; the record knows the repository only by UUID", record.DisplayName)
	}
}
