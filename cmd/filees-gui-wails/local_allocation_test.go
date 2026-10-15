package main

import (
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/localallocation"
	"path/filepath"
	"testing"
)

func TestLocalAllocationUsesOnlyAttachedRootsAndLocalActivity(t *testing.T) {
	path := t.TempDir()
	vm := guiapp.ViewModel{Repos: []guiapp.RepoViewModel{{ServerID: "s", ID: "r", Attached: true, LocalPath: path}, {ServerID: "s", ID: "remote"}}, Activity: []guiapp.ActivityViewModel{{RepoID: "r", Path: "dir/file", UpdatedAt: "2026-10-15"}, {RepoID: "r", Path: "../outside", UpdatedAt: "later"}}}
	roots, busy := allocationRoots(vm)
	if busy || len(roots) != 1 || roots[0].Hints["dir"] != "2026-10-15" || len(roots[0].Hints) != 3 {
		t.Fatalf("%+v busy=%v", roots, busy)
	}
	manager := localallocation.New(filepath.Join(t.TempDir(), "cache.json"))
	manager.Update(roots, false)
	snapshot := Snapshot{Connected: true, Servers: []ServerProjection{{ID: "s", RepositoriesReady: true}, {ID: "empty", RepositoriesReady: true}}}
	applyLocalAllocation(&snapshot, manager)
	if snapshot.Servers[0].LocalAllocation.Known || !snapshot.Servers[1].LocalAllocation.Known {
		t.Fatalf("%+v", snapshot.Servers)
	}
}
