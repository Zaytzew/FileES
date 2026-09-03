package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/localrepo"
)

// An operation with no lifecycle record at all cannot be reconciled, and saying
// so must not look like a fault.
//
// On the owner's machine one provisioning journal entry outlived the repository
// it described, and startup reported it as an error every single time - the same
// shape as A11 in the seam register, a terminal condition landing in a branch
// that repeats. A permanent false alarm at startup is worse than silence: it
// teaches the reader to skip the one place where real failures appear, and this
// project has already lost a day to a failure nobody could see.
//
// The distinction is asserted at the store, because that is what the caller
// switches on: os.ErrNotExist means orphaned, anything else means broken.
func TestAMissingLifecycleRecordIsReportedAsAbsenceNotFailure(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "repository-lifecycle.json"))
	if err != nil {
		t.Fatalf("open lifecycle: %v", err)
	}
	_, err = store.MarkRepositoryCreated("86a27f2b-7d86-48a4-a998-41b3f5fb4d7e", "repo", "svn+ssh://x/repo")
	if err == nil {
		t.Fatal("marking an unknown operation succeeded")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v; the caller cannot tell an orphan from a real fault", err)
	}
}

// And a record that exists but is in a state the reconciliation must not roll
// back still reports a real error rather than being mistaken for an orphan.
func TestARealReconciliationFaultIsNotMistakenForAnOrphan(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "repository-lifecycle.json"))
	if err != nil {
		t.Fatalf("open lifecycle: %v", err)
	}
	created, err := store.BeginCreate("manual", "Willa", filepath.Join(t.TempDir(), "Willa"))
	if err != nil {
		t.Fatalf("begin create: %v", err)
	}
	// A repository ID containing a separator is rejected by validation, which
	// is a fault in the operation rather than an absence.
	_, err = store.MarkRepositoryCreated(created.OperationID, "bad/id", "svn+ssh://x/bad")
	if err == nil {
		t.Fatal("an invalid repository ID was accepted")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v; a real fault would be silently skipped as an orphan", err)
	}
}
