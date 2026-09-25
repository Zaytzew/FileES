package app

import "testing"

// Owner's production, 2026-09-25: the folder read "wymaga uwagi" while its
// change waited for a passport; the reason was only in the journal.
func TestFolderProblemNamesTheWaitingFileAndTheLatestReason(t *testing.T) {
	vm := ViewModel{Errors: []ErrorViewModel{
		{ID: "passport:cloud:krc:b", RepoID: "krc", Timestamp: "2026-09-25T08:43:54Z", MessageDetail: "01_PROJEKT/B.dwg"},
		{ID: "passport:cloud:krc:a", RepoID: "krc", Timestamp: "2026-09-25T08:28:24Z", MessageDetail: "01_PROJEKT/KRCPL_komplet.dwg"},
		{ID: "e1", RepoID: "krc", Timestamp: "2026-09-25T08:44:54Z", Code: "LOCK-2104", MessageKey: "passport.replacement_unavailable", Message: "Usługa zmiany rezerwacji jest niedostępna"},
		{ID: "e2", RepoID: "krc", Timestamp: "2026-09-25T08:45:53Z", Code: "LOCK-2106", MessageKey: "passport.path_owner_unavailable", Message: "passport.path_owner_unavailable"},
		{ID: "e3", RepoID: "krc", Timestamp: "2026-09-25T08:50:00Z", Code: "NET-4008", MessageKey: "net.connection_dropped", Message: "Połączenie zerwane"},
		{ID: "passport:cloud:other:x", RepoID: "other", Timestamp: "2026-09-25T08:00:00Z", MessageDetail: "x"},
	}}
	problem, ok := vm.FolderProblem("krc")
	if !ok || problem.Kind != FolderProblemBorrowPending {
		t.Fatalf("problem=%+v ok=%v", problem, ok)
	}
	if problem.Path != "01_PROJEKT/KRCPL_komplet.dwg" || problem.More != 1 || problem.Since != "2026-09-25T08:28:24Z" {
		t.Fatalf("waiting file: %+v", problem)
	}
	// The latest passport reason; an unrendered key is not shown as a sentence,
	// and a network error is not a passport reason.
	if problem.Code != "LOCK-2106" || problem.Reason != "" {
		t.Fatalf("reason: %+v", problem)
	}
	if _, ok := vm.FolderProblem("quiet"); ok {
		t.Fatal("a folder without waiting passports has a problem")
	}
	// Journal reasons alone, with no passport waiting, are history.
	history := ViewModel{Errors: vm.Errors[2:5]}
	if _, ok := history.FolderProblem("krc"); ok {
		t.Fatal("old journal errors produced a problem after recovery")
	}
}

// KRAŃCOWA-PŁOŃSK, 2026-09-25: another client had the file reserved, and the
// card said only that the owner could not be confirmed.
func TestFolderProblemNamesWhoElseHoldsTheFile(t *testing.T) {
	vm := ViewModel{
		Errors: []ErrorViewModel{{ID: "passport:cloud:krc:a", RepoID: "krc", Timestamp: "2026-09-25T16:40:00Z", MessageDetail: "01_PROJEKT-BUDOWLANY/KRCPL_komplet.dwg"}},
		Reservations: []Reservation{
			{RepoID: "krc", Path: "01_PROJEKT-BUDOWLANY/KRCPL_komplet.dwg", OwnerLabel: "mine", CanRelease: true},
			{RepoID: "other", Path: "01_PROJEKT-BUDOWLANY/KRCPL_komplet.dwg", OwnerLabel: "elsewhere"},
			{RepoID: "krc", Path: "/01_PROJEKT-BUDOWLANY/KRCPL_komplet.dwg", OwnerLabel: "biuro:jan", CreatedAt: "2026-09-25T16:30:00Z"},
		},
	}
	problem, ok := vm.FolderProblem("krc")
	if !ok || problem.Holder != "biuro:jan" || problem.HolderSince != "2026-09-25T16:30:00Z" {
		t.Fatalf("holder: %+v", problem)
	}
}
