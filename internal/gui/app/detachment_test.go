package app

import (
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

// The reducer is the only path by which the moment reaches any presentation at
// all, so it has to carry the daemon's list through untouched.
func TestTheReducerCarriesDetachmentsFromTheDaemon(t *testing.T) {
	system := contract.SystemStatusResult{Detachments: []contract.Detachment{{
		ServerID: "manual", DisplayName: "manual", Address: "manual.example",
		Cause: "self", At: "2026-09-03T17:40:00Z",
		WorkingCopies: []string{`C:\Projekty\Willa`},
	}}}
	vm := newAppState().applyFullSnapshot(system, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, time.Now()).viewModel()
	if len(vm.Detachments) != 1 {
		t.Fatalf("view model detachments = %d, want 1", len(vm.Detachments))
	}
	got := vm.Detachments[0]
	if !got.SelfDetached() || got.Name() != "manual" || got.Address != "manual.example" {
		t.Fatalf("detachment = %+v", got)
	}
	if len(got.WorkingCopies) != 1 || got.WorkingCopies[0] != `C:\Projekty\Willa` {
		t.Fatalf("working copies = %v", got.WorkingCopies)
	}
}

// A detached server leaves the projection, and its detachment record is the
// only thing left that names it. The two must not fight: the record is not a
// server row and must never resurrect one.
func TestADetachmentDoesNotPutTheServerBackInTheProjection(t *testing.T) {
	system := contract.SystemStatusResult{
		Activations: []contract.ActivationStatus{{ServerID: "manual", DisplayName: "manual", Detached: true}},
		Detachments: []contract.Detachment{{ServerID: "manual", DisplayName: "manual", Cause: "revoked", At: "2026-09-03T18:10:00Z"}},
	}
	vm := newAppState().applyFullSnapshot(system, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, false, time.Now()).viewModel()
	if len(vm.Servers) != 0 {
		t.Fatalf("servers = %d, want 0: a detached server leaves the view with its repositories", len(vm.Servers))
	}
	if len(vm.Detachments) != 1 {
		t.Fatalf("detachments = %d, want 1", len(vm.Detachments))
	}
}

func TestDetachmentNameFallsBackToTheServerID(t *testing.T) {
	if got := (DetachmentViewModel{ServerID: "atmprojekt:filees"}).Name(); got != "atmprojekt:filees" {
		t.Fatalf("Name() = %q", got)
	}
	if got := (DetachmentViewModel{ServerID: "id", DisplayName: " cloud "}).Name(); got != "cloud" {
		t.Fatalf("Name() = %q", got)
	}
}
