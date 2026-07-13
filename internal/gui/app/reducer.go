package app

import contract "filees/pkg/contract/v1"

// appState is the internal state of the App event loop.
// All methods are pure — they return a new appState and have no side effects.
// Only the event loop goroutine accesses appState; no locking needed.
type appState struct {
	connected bool
	stale     bool
	caps      map[string]bool
	summaries map[string]contract.RepoSummary // from repo.list; carries URL + LocalPath
	snapshots map[string]contract.RepoStatus  // from repo.status; carries live state
	order     []string                        // repoID insertion order from repo.list
	lastSeq   int64                           // last event sequence number received
}

func newAppState() appState {
	return appState{
		caps:      make(map[string]bool),
		summaries: make(map[string]contract.RepoSummary),
		snapshots: make(map[string]contract.RepoStatus),
	}
}

// applyConnected records a successful connection with its capability set.
func (s appState) applyConnected(caps []string) appState {
	s.connected = true
	s.stale = false
	capMap := make(map[string]bool, len(caps))
	for _, c := range caps {
		capMap[c] = true
	}
	s.caps = capMap
	return s
}

// applyDisconnected marks the state as disconnected and stale.
// Snapshots and order are preserved so the tray can show the last-known state.
func (s appState) applyDisconnected() appState {
	s.connected = false
	s.stale = true
	s.caps = make(map[string]bool)
	s.lastSeq = 0
	return s
}

// applyRepoList sets the canonical repository order and initialises missing summary entries.
func (s appState) applyRepoList(repos []contract.RepoSummary) appState {
	newSummaries := make(map[string]contract.RepoSummary, len(repos))
	order := make([]string, 0, len(repos))
	for _, r := range repos {
		newSummaries[r.ID] = r
		order = append(order, r.ID)
	}
	s.summaries = newSummaries
	s.order = order
	return s
}

// applySnapshot stores or replaces a live repo status snapshot.
func (s appState) applySnapshot(status contract.RepoStatus) appState {
	next := make(map[string]contract.RepoStatus, len(s.snapshots)+1)
	for k, v := range s.snapshots {
		next[k] = v
	}
	next[status.RepoID] = status
	s.snapshots = next
	return s
}

// applyEvent updates the last-seen sequence and returns:
//   - the new state
//   - needsResync: true when a gap in the sequence stream is detected
//   - dirtyRepo: the repoID to refresh (empty when needsResync is true)
func (s appState) applyEvent(ev contract.Event) (appState, bool, string) {
	gap := s.lastSeq > 0 && ev.Sequence != s.lastSeq+1
	if ev.Sequence > s.lastSeq {
		s.lastSeq = ev.Sequence
	}
	if gap {
		return s, true, ""
	}
	return s, false, ev.RepoID
}

// viewModel builds the read-only ViewModel from the current state.
// Called from the event loop after every state transition.
func (s appState) viewModel() ViewModel {
	repos := make([]RepoViewModel, 0, len(s.order))
	for _, id := range s.order {
		sum := s.summaries[id]
		snap := s.snapshots[id]
		repos = append(repos, RepoViewModel{
			ID:           id,
			URL:          sum.URL,
			LocalPath:    sum.LocalPath,
			State:        snap.State,
			Connectivity: snap.Connectivity,
			LocalRev:     snap.LocalRevision,
			HeadRev:      snap.HeadRevision,
			Pending:      snap.Pending,
			Conflicts:    snap.Conflicts,
			LastSyncAt:   snap.LastSyncAt,
			CurrentOp:    snap.CurrentOperation,
		})
	}
	caps := make(map[string]bool, len(s.caps))
	for k, v := range s.caps {
		caps[k] = v
	}
	vm := ViewModel{
		Connected:    s.connected,
		Stale:        s.stale,
		Capabilities: caps,
		Repos:        repos,
	}
	vm.Icon = aggregateIcon(s.connected, repos)
	return vm
}
