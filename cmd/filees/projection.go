package main

import (
	"sort"

	"filees/pkg/clientview"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
)

// attachedProjection converts server authority into supervisor input. Entries
// without a local attachment remain visible control-plane knowledge, but do
// not start a data pipeline until the user chooses a local working copy.
func attachedProjection(serverID string, view clientview.View, attachments map[reposupervisor.Key]repoRuntime) []reposupervisor.Desired {
	desired := make([]reposupervisor.Desired, 0, len(view.Repositories))
	for _, repo := range view.Repositories {
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		if _, attached := attachments[key]; !attached {
			continue
		}
		desired = append(desired, reposupervisor.Desired{Key: key, Access: repo.Access, State: repo.State, URL: repo.URL, DisplayName: repo.DisplayName})
	}
	sort.Slice(desired, func(i, j int) bool { return desired[i].Key.String() < desired[j].Key.String() })
	return desired
}

func syncProjectionKnowledge(ipc *ipcserver.Server, serverID string, view clientview.View, attachments map[reposupervisor.Key]repoRuntime) {
	if ipc == nil {
		return
	}
	projected := make([]ipcserver.ProjectedRepo, 0, len(view.Repositories))
	for _, repo := range view.Repositories {
		key := reposupervisor.Key{ServerID: serverID, RepoID: repo.RepoID}
		_, attached := attachments[key]
		projected = append(projected, ipcserver.ProjectedRepo{ID: repo.RepoID, DisplayName: repo.DisplayName, URL: repo.URL, Access: repo.Access, State: repo.State, Attached: attached})
	}
	ipc.ReconcileProjectedRepos(serverID, projected)
}
