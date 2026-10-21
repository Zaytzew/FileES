package app

import contract "filees/pkg/contract/v1"

// applyRefreshInventory exposes local inventory before supplementary lists
// finish. It deliberately preserves stale and action fences: only the final
// full snapshot grants fresh authority. Routine refreshes keep their current
// stable queue header; initial/reconnected sessions count newly observed data.
func (s appState) applyRefreshInventory(system contract.SystemStatusResult, repos []contract.RepoSummary) appState {
	s = s.applyRepoList(repos)
	s.system = system
	s = s.rememberServerOrder(system, repos, nil)
	if s.stale {
		s.queueScanning = true
		s.queueScanned = make(map[string]contract.RepoStatus)
	}
	return s
}

func (s appState) applyRefreshProgress(statuses []contract.RepoStatus) appState {
	s = s.applySnapshots(statuses)
	if s.queueScanning {
		next := make(map[string]contract.RepoStatus, len(s.queueScanned)+len(statuses))
		for id, status := range s.queueScanned {
			next[id] = status
		}
		for _, status := range statuses {
			next[status.RepoID] = status
		}
		s.queueScanned = next
	}
	return s
}
