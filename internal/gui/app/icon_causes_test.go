package app

import "testing"

func TestIconCausesUseSamePriorityAndIgnoreUnattachedRemoteRepos(t *testing.T) {
	vm := ViewModel{Connected: true, Repos: []RepoViewModel{
		{ID: "clean", LocalCopyPreserved: true, LocalCopyStatus: "clean"},
		{ID: "remote", State: "initializing", AttachmentPolicy: "optional"},
		{ID: "offline", Attached: true, Connectivity: "offline"},
		{ID: "dirty", LocalCopyPreserved: true, LocalCopyStatus: "changed"},
	}}
	vm.Icon = aggregateIcon(true, vm.Repos, 0)
	got := vm.IconCauses()
	if len(got) != 1 || got[0].RepoID != "dirty" || got[0].Reason != "preserved_copy_changed" {
		t.Fatalf("causes: %+v", got)
	}
	vm.Connected = false
	if got := vm.IconCauses(); len(got) != 1 || got[0].Reason != "daemon_offline" {
		t.Fatal(got)
	}
	vm.Connected = true
	vm.Stale = true
	if got := vm.IconCauses(); len(got) != 1 || got[0].Reason != "refreshing" {
		t.Fatal(got)
	}
	vm.Stale = false
	vm.Icon = IconShout
	if got := vm.IconCauses(); len(got) != 1 || got[0].Reason != "announcements" {
		t.Fatal(got)
	}
}
