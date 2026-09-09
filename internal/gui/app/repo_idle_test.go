package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestCanFoldInactiveProtectsLiveWork(t *testing.T) {
	vm := ViewModel{Connected: true}
	repo := RepoViewModel{ID: "r", ServerID: "s", Attached: true, State: contract.StateActive, Connectivity: contract.ConnOnline}
	if !vm.CanFoldInactive(repo) {
		t.Fatalf("quiet repo not eligible: %+v", repo)
	}
	vm.Stale = true
	if vm.CanFoldInactive(repo) {
		t.Fatal("stale hidden")
	}
	vm.Stale = false
	vm.Servers = []ServerViewModel{{ID: "s"}}
	if vm.CanFoldInactive(repo) {
		t.Fatal("unverified server hidden")
	}
	vm.Servers[0].ViewSyncedAt = "2026-09-09T12:00:00Z"
	if !vm.CanFoldInactive(repo) {
		t.Fatal("verified server not eligible")
	}
	vm.PendingActions = []PendingAction{{ServerID: "s", RepoID: "r"}}
	if vm.CanFoldInactive(repo) {
		t.Fatal("action hidden")
	}
	vm.PendingActions = nil
	vm.Notices = []NoticeViewModel{{RepoID: "r"}}
	if vm.CanFoldInactive(repo) {
		t.Fatal("shout hidden")
	}
	vm.Notices = nil
	repo.Pending.Modified = 1
	if vm.CanFoldInactive(repo) {
		t.Fatal("queue hidden")
	}
	repo.Pending.Modified = 0
	repo.Conflicts = 1
	if vm.CanFoldInactive(repo) {
		t.Fatal("conflict hidden")
	}
	repo.Conflicts = 0
	repo.Attached = false
	if vm.CanFoldInactive(repo) {
		t.Fatal("remote hidden")
	}
}
