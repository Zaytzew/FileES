package main

import (
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/detachment"
	"filees/pkg/localrepo"
)

func openDetachments(t *testing.T) *detachment.Store {
	t.Helper()
	store, err := detachment.Open(filepath.Join(t.TempDir(), "server-detachments.json"))
	if err != nil {
		t.Fatalf("open detachments: %v", err)
	}
	return store
}

func TestTheSourceRendersWhatThePanelWillShow(t *testing.T) {
	store := openDetachments(t)
	at := time.Date(2026, 9, 3, 17, 40, 0, 0, time.UTC)
	if err := store.Record(detachment.Record{
		ServerID: "manual", DisplayName: "manual", Address: "manual.example",
		Cause: detachment.CauseSelf, At: at, WorkingCopies: []string{`C:\Projekty\Willa`},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := detachmentSource{store: store}.List()
	if len(got) != 1 {
		t.Fatalf("List = %d, want 1", len(got))
	}
	if got[0].ServerID != "manual" || got[0].Cause != "self" || got[0].Address != "manual.example" {
		t.Fatalf("detachment = %+v", got[0])
	}
	if got[0].At != "2026-09-03T17:40:00Z" {
		t.Fatalf("At = %q, want RFC3339 UTC", got[0].At)
	}
	if len(got[0].WorkingCopies) != 1 {
		t.Fatalf("working copies = %v", got[0].WorkingCopies)
	}
}

func TestAnAbsentStoreListsNothingRatherThanFailing(t *testing.T) {
	// The daemon starts without the store if it could not be opened. A
	// missing chronology is a far better outcome than a client that will not
	// run, so every consumer has to tolerate the nil.
	if got := (detachmentSource{}).List(); len(got) != 0 {
		t.Fatalf("List = %v, want empty", got)
	}
}

func TestNoticingTheSameRefusalAgainDoesNotSlideTheMoment(t *testing.T) {
	store := openDetachments(t)
	first := time.Date(2026, 9, 3, 18, 10, 0, 0, time.UTC)
	rec := detachment.Record{ServerID: "manual", Cause: detachment.CauseRevoked, At: first}
	if written, err := store.RecordFirstNoticed(rec); err != nil || !written {
		t.Fatalf("first RecordFirstNoticed = %v, %v; want true, nil", written, err)
	}
	// The refusal arrives again on every polling cycle for as long as it
	// lasts. Recording each one would push the moment forward minute by minute
	// and the forty-eight hour lifetime would never expire.
	rec.At = first.Add(time.Hour)
	if written, err := store.RecordFirstNoticed(rec); err != nil || written {
		t.Fatalf("second RecordFirstNoticed = %v, %v; want false, nil", written, err)
	}
	got := store.ListAt(first.Add(2 * time.Hour))
	if len(got) != 1 || !got[0].At.Equal(first) {
		t.Fatalf("record = %+v; the honest answer is when we first noticed", got)
	}
}

func TestWorkingCopiesAreTheFoldersLeftOnThisDisk(t *testing.T) {
	store, err := localrepo.Open(filepath.Join(t.TempDir(), "repository-lifecycle.json"))
	if err != nil {
		t.Fatalf("open lifecycle: %v", err)
	}
	attach := func(serverID, repoID, path string) {
		t.Helper()
		if _, _, err := store.EnsureConfiguredAttached(serverID, repoID, "svn+ssh://x/"+repoID, "rw", path, repoID); err != nil {
			t.Fatalf("seed %s: %v", repoID, err)
		}
	}
	finish := func(serverID, repoID string, deleteRepository bool) {
		t.Helper()
		started, err := store.BeginDetach(serverID, repoID, deleteRepository)
		if err != nil {
			t.Fatalf("begin detach %s: %v", repoID, err)
		}
		if deleteRepository {
			retainUntil := time.Now().Add(720 * time.Hour).UTC().Format(time.RFC3339)
			if _, err := store.MarkServerDeleted(started.OperationID, retainUntil); err != nil {
				t.Fatalf("mark deleted %s: %v", repoID, err)
			}
			if _, err := store.MarkRecoveryPrepared(started.OperationID, filepath.Join(t.TempDir(), "kit")); err != nil {
				t.Fatalf("mark recovery prepared %s: %v", repoID, err)
			}
			if _, err := store.MarkLocalCleanupCompleted(started.OperationID); err != nil {
				t.Fatalf("mark cleanup %s: %v", repoID, err)
			}
		}
		if _, err := store.CompleteDetach(started.OperationID); err != nil {
			t.Fatalf("complete detach %s: %v", repoID, err)
		}
	}
	attach("manual", "willa", `C:\Projekty\Willa`)
	attach("manual", "biurowiec", `C:\Projekty\Biurowiec`)
	finish("manual", "biurowiec", false)
	// A deleted repository has no working copy any more; offering the path
	// would send someone to an empty place.
	attach("manual", "stare", `C:\Projekty\Stare`)
	finish("manual", "stare", true)
	attach("spot", "obce", `C:\Projekty\Obce`)

	got := workingCopiesOf(store, "manual")
	if len(got) != 2 {
		t.Fatalf("working copies = %v, want the two that still exist", got)
	}
	for _, unwanted := range []string{`C:\Projekty\Stare`, `C:\Projekty\Obce`} {
		for _, path := range got {
			if path == unwanted {
				t.Errorf("working copies include %s", unwanted)
			}
		}
	}
}

func TestWorkingCopiesToleratesAnAbsentLifecycleStore(t *testing.T) {
	if got := workingCopiesOf(nil, "manual"); got != nil {
		t.Fatalf("workingCopiesOf(nil) = %v, want nil", got)
	}
}
