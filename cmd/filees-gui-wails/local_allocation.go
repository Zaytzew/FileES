package main

import (
	"path/filepath"
	"strconv"
	"strings"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/localallocation"
)

func allocationRoots(vm guiapp.ViewModel) ([]localallocation.Root, bool) {
	roots := []localallocation.Root{}
	busy := len(vm.PendingActions) > 0
	for _, r := range vm.Repos {
		if r.PublishProgress != nil || r.ReconcileProgress != nil || (r.CurrentOp != nil && *r.CurrentOp != "working_copy_missing") {
			busy = true
		}
		if r.LocalPath == "" || (!r.Attached && !r.LocallyProvisioning()) {
			continue
		}
		root := localallocation.Root{Server: r.ServerID, Repo: r.ID, Path: r.LocalPath, Hints: map[string]string{}}
		token := strconv.FormatInt(r.LocalRev, 10) + ":" + r.LastCommitAt
		root.Hints[".svn"] = token
		root.Hints[".filees"] = token
		for _, a := range vm.Activity {
			if a.RepoID != r.ID || a.Path == "" {
				continue
			}
			path := filepath.FromSlash(a.Path)
			if filepath.IsAbs(path) {
				var err error
				path, err = filepath.Rel(r.LocalPath, path)
				if err != nil {
					continue
				}
			}
			if !filepath.IsLocal(path) {
				continue
			}
			parent := filepath.Dir(path)
			if a.UpdatedAt > root.Hints[parent] {
				root.Hints[parent] = a.UpdatedAt
			}
		}
		roots = append(roots, root)
	}
	return roots, busy
}

func applyLocalAllocation(snapshot *Snapshot, manager *localallocation.Manager) {
	if manager == nil {
		return
	}
	values := manager.Values()
	for i := range snapshot.Servers {
		value, ok := values[snapshot.Servers[i].ID]
		if !ok {
			// A confirmed server with no attached copies uses no space in this scope.
			value = localallocation.Value{Known: snapshot.Connected && snapshot.Servers[i].RepositoriesReady, State: "waiting"}
			if value.Known {
				value.State = "ready"
			}
		}
		snapshot.Servers[i].LocalAllocation = &value
	}
}

func (service *GUIService) allocationChanged() {
	service.mu.Lock()
	next := service.snapshot
	next.Servers = append([]ServerProjection(nil), next.Servers...)
	applyLocalAllocation(&next, service.localAllocation)
	next.Revision++
	service.snapshot = next
	emitter := service.emitter
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(snapshotEvent, next)
	}
}

// No path-taking Wails method is exposed. Roots come only from daemon facts.
func (service *GUIService) attachLocalAllocation(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	service.localAllocation = localallocation.New(path)
}
