package app

import (
	"time"

	contract "filees/pkg/contract/v1"
)

// appState is the internal state of the App event loop.
// All methods are pure — they return a new appState and have no side effects.
// Only the event loop goroutine accesses appState; no locking needed.
type appState struct {
	connected          bool
	stale              bool
	caps               map[string]bool
	summaries          map[string]contract.RepoSummary // from repo.list; carries URL + LocalPath
	snapshots          map[string]contract.RepoStatus  // from repo.status; carries live state
	order              []string                        // stable repoID first-seen order
	serverOrder        []string                        // stable serverID first-seen order
	lastSeq            int64                           // last event sequence number received
	system             contract.SystemStatusResult
	errors             []ErrorViewModel
	activity           []ActivityViewModel
	reservations       map[string]int
	repoReservations   map[string]int
	reservationItems   []Reservation
	reservationsKnown  bool
	notices            []NoticeViewModel
	pendingActions     map[string]PendingAction
	pendingActionOrder []string
	refreshed          time.Time
}

func newAppState() appState {
	return appState{
		caps:             make(map[string]bool),
		summaries:        make(map[string]contract.RepoSummary),
		snapshots:        make(map[string]contract.RepoStatus),
		reservations:     make(map[string]int),
		repoReservations: make(map[string]int),
		pendingActions:   make(map[string]PendingAction),
	}
}

// applyConnected records a successful connection with its capability set.
func (s appState) applyConnected(caps []string) appState {
	s.connected = true
	// A successful handshake proves connectivity, but the previous snapshot is
	// stale until the complete system/repository refresh succeeds.
	s.stale = true
	capMap := make(map[string]bool, len(caps))
	for _, c := range caps {
		capMap[c] = true
	}
	s.caps = capMap
	return s
}

// applyFullSnapshot atomically replaces all authoritative daemon/repository
// data and marks it fresh. Removed repositories and their old snapshots are
// pruned as part of the replacement.
func (s appState) applyFullSnapshot(system contract.SystemStatusResult, repos []contract.RepoSummary, statuses []contract.RepoStatus, records []contract.ErrorRecord, activityRecords []contract.ActivityRecord, reservationCounts, repoReservationCounts map[string]int, reservationItems []Reservation, reservationsKnown bool, notices []contract.Notice, refreshed time.Time) appState {
	s = s.applyRepoList(repos)
	next := make(map[string]contract.RepoStatus, len(statuses))
	for _, status := range statuses {
		next[status.RepoID] = status
	}
	s.snapshots = next
	s.system = system
	s = s.rememberServerOrder(system, repos, statuses)
	s.errors = make([]ErrorViewModel, 0, len(records))
	for _, record := range records {
		s.errors = append(s.errors, ErrorViewModel{
			ID: record.ID, RepoID: record.RepoID, Timestamp: record.TS,
			Code: record.Code, Severity: record.Severity, Hint: record.Hint, Message: record.Msg,
		})
	}
	s.activity = make([]ActivityViewModel, 0, len(activityRecords))
	for _, record := range activityRecords {
		s.activity = append(s.activity, ActivityViewModel{RepoID: record.RepoID, Path: record.Path, Kind: record.Kind, Stage: record.Stage, UpdatedAt: record.UpdatedAt, Revision: record.Revision, ErrorID: record.ErrorID})
	}
	s.reservations = make(map[string]int, len(reservationCounts))
	for serverID, count := range reservationCounts {
		s.reservations[serverID] = count
	}
	s.repoReservations = make(map[string]int, len(repoReservationCounts))
	for key, count := range repoReservationCounts {
		s.repoReservations[key] = count
	}
	s.reservationItems = append([]Reservation(nil), reservationItems...)
	s.reservationsKnown = reservationsKnown
	s.notices = make([]NoticeViewModel, 0, len(notices))
	for _, notice := range notices {
		if notice.Acked {
			continue
		}
		s.notices = append(s.notices, NoticeViewModel{ID: notice.ID, RepoID: notice.RepoID, Title: notice.Title, CreatedAt: notice.CreatedAt})
	}
	s.refreshed = refreshed
	s.stale = false
	return s
}

// applySnapshots atomically applies a coalesced partial repository refresh.
func (s appState) applySnapshots(statuses []contract.RepoStatus) appState {
	next := make(map[string]contract.RepoStatus, len(s.snapshots)+len(statuses))
	for k, v := range s.snapshots {
		next[k] = v
	}
	for _, status := range statuses {
		next[status.RepoID] = status
	}
	s.snapshots = next
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

func (s appState) applyStale() appState {
	s.stale = true
	return s
}

// applyRepoList replaces the current repository inventory while preserving the
// first-seen presentation slot of every repository. A temporarily absent repo
// is not rendered, but keeps its rank if a later snapshot brings it back.
func (s appState) applyRepoList(repos []contract.RepoSummary) appState {
	newSummaries := make(map[string]contract.RepoSummary, len(repos))
	known := make(map[string]bool, len(s.order)+len(repos))
	for _, id := range s.order {
		known[id] = true
	}
	for _, r := range repos {
		newSummaries[r.ID] = r
		if !known[r.ID] {
			s.order = append(s.order, r.ID)
			known[r.ID] = true
		}
	}
	s.summaries = newSummaries
	return s
}

func (s appState) rememberServerOrder(system contract.SystemStatusResult, repos []contract.RepoSummary, statuses []contract.RepoStatus) appState {
	known := make(map[string]bool, len(s.serverOrder)+len(system.Activations)+len(repos)+len(statuses))
	for _, id := range s.serverOrder {
		known[id] = true
	}
	remember := func(id string) {
		if id != "" && !known[id] {
			s.serverOrder = append(s.serverOrder, id)
			known[id] = true
		}
	}
	for _, activation := range system.Activations {
		remember(activation.ServerID)
	}
	for _, repo := range repos {
		remember(repo.ServerID)
	}
	for _, status := range statuses {
		remember(status.ServerID)
	}
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
	if ev.Type == contract.EvNoticeCreated {
		return s, true, ""
	}
	return s, false, ev.RepoID
}

// viewModel builds the read-only ViewModel from the current state.
// Called from the event loop after every state transition.
func (s appState) viewModel() ViewModel {
	repos := make([]RepoViewModel, 0, len(s.order))
	for _, id := range s.order {
		sum, present := s.summaries[id]
		if !present {
			continue
		}
		snap := s.snapshots[id]
		serverID := snap.ServerID
		if serverID == "" {
			serverID = sum.ServerID
		}
		repos = append(repos, RepoViewModel{
			ID:               id,
			DisplayName:      sum.DisplayName,
			ServerID:         serverID,
			Attached:         sum.Attached,
			Access:           snap.Access,
			OwnerRealmID:     snap.OwnerRealmID,
			AttachmentPolicy: snap.AttachmentPolicy,
			EditingPolicy:    snap.EditingPolicy,
			URL:              sum.URL,
			LocalPath:        sum.LocalPath,
			State:            snap.State,
			Connectivity:     snap.Connectivity,
			LocalRev:         snap.LocalRevision,
			HeadRev:          snap.HeadRevision,
			Pending:          snap.Pending,
			Conflicts:        snap.Conflicts,
			LastSyncAt:       snap.LastSyncAt,
			CurrentOp:        snap.CurrentOperation,
			ReservationCount: s.repoReservations[reservationKey(sum.ServerID, sum.ID)],
			Cycle:            snap.Cycle,
		})
	}
	serverByID := make(map[string]ServerViewModel, len(s.system.Activations))
	for _, activation := range s.system.Activations {
		serverByID[activation.ServerID] = ServerViewModel{ID: activation.ServerID, DisplayName: activation.DisplayName, ClientRole: activation.ClientRole, RealmID: activation.RealmID, RealmAlias: activation.RealmAlias, Address: activation.Address, ClientID: activation.ClientID, SSHPort: activation.SSHPort, CanCreateRepositories: activation.CanCreateRepositories, RepositoriesReady: activation.RepositoriesReady, PendingRequiredRepos: activation.PendingRequiredRepos, SessionTimeoutMin: activation.SessionTimeoutMin, ReservationCount: s.reservations[activation.ServerID], ReservationsKnown: s.reservationsKnown}
	}
	for _, repo := range repos {
		server, ok := serverByID[repo.ServerID]
		if !ok {
			server = ServerViewModel{ID: repo.ServerID, DisplayName: repo.ServerID, ReservationCount: s.reservations[repo.ServerID], ReservationsKnown: s.reservationsKnown}
		}
		server.Repos = append(server.Repos, repo)
		serverByID[repo.ServerID] = server
	}
	servers := make([]ServerViewModel, 0, len(serverByID))
	appended := make(map[string]bool, len(serverByID))
	for _, id := range s.serverOrder {
		if server, present := serverByID[id]; present {
			servers = append(servers, server)
			appended[id] = true
		}
	}
	// Keep viewModel useful in focused reducer tests and for any future partial
	// state construction that did not pass through applyFullSnapshot.
	for _, activation := range s.system.Activations {
		if !appended[activation.ServerID] {
			servers = append(servers, serverByID[activation.ServerID])
			appended[activation.ServerID] = true
		}
	}
	for _, repo := range repos {
		if !appended[repo.ServerID] {
			servers = append(servers, serverByID[repo.ServerID])
			appended[repo.ServerID] = true
		}
	}
	caps := make(map[string]bool, len(s.caps))
	for k, v := range s.caps {
		caps[k] = v
	}
	vm := ViewModel{
		Connected:      s.connected,
		Stale:          s.stale,
		DaemonState:    s.system.State,
		UptimeSec:      s.system.UptimeSec,
		LastRefresh:    s.refreshed,
		Capabilities:   caps,
		Repos:          repos,
		Servers:        servers,
		Reservations:   append([]Reservation(nil), s.reservationItems...),
		Errors:         append([]ErrorViewModel(nil), s.errors...),
		Activity:       append([]ActivityViewModel(nil), s.activity...),
		Notices:        append([]NoticeViewModel(nil), s.notices...),
		PendingActions: s.projectPendingActions(),
	}
	now := time.Now().UTC()
	for _, recovery := range s.system.Recoveries {
		downloadUntil, _ := time.Parse(time.RFC3339Nano, recovery.DownloadUntil)
		vm.Recoveries = append(vm.Recoveries, RecoveryViewModel{
			OperationID: recovery.OperationID, ServerID: recovery.ServerID, ServerName: recovery.ServerName,
			KitPath: recovery.KitPath, AdminContact: recovery.AdminContact, ArchiveCount: recovery.ArchiveCount,
			DownloadUntil: recovery.DownloadUntil, AdminGraceUntil: recovery.AdminGraceUntil,
			CanDownload: now.Before(downloadUntil),
		})
	}
	if update := s.system.Update; update != nil {
		vm.Update = &UpdateViewModel{
			State: update.State, CurrentVersion: update.CurrentVersion,
			AvailableVersion: update.AvailableVersion, ReleaseID: update.ReleaseID,
			Summary: update.Summary, RestartRequired: update.RestartRequired,
		}
	}
	if s.connected && s.stale {
		vm.Icon = IconBusy
	} else {
		vm.Icon = aggregateIcon(s.connected, repos, len(s.notices))
	}
	return vm
}

func (s appState) startPendingAction(action PendingAction) appState {
	if action.ID == "" {
		return s
	}
	next := make(map[string]PendingAction, len(s.pendingActions)+1)
	for id, pending := range s.pendingActions {
		next[id] = pending
	}
	if _, exists := next[action.ID]; !exists {
		s.pendingActionOrder = append(s.pendingActionOrder, action.ID)
	}
	action.Phase = ActionRunning
	next[action.ID] = action
	s.pendingActions = next
	return s
}

func (s appState) awaitPendingAction(id string) appState {
	action, exists := s.pendingActions[id]
	if !exists {
		return s
	}
	next := make(map[string]PendingAction, len(s.pendingActions))
	for key, pending := range s.pendingActions {
		next[key] = pending
	}
	action.Phase = ActionAwaitingProjection
	next[id] = action
	s.pendingActions = next
	return s
}

func (s appState) finishPendingActions(ids []string) appState {
	if len(ids) == 0 || len(s.pendingActions) == 0 {
		return s
	}
	next := make(map[string]PendingAction, len(s.pendingActions))
	for id, pending := range s.pendingActions {
		next[id] = pending
	}
	removed := make(map[string]bool, len(ids))
	for _, id := range ids {
		delete(next, id)
		removed[id] = true
	}
	s.pendingActions = next
	order := make([]string, 0, len(s.pendingActionOrder))
	for _, id := range s.pendingActionOrder {
		if !removed[id] {
			order = append(order, id)
		}
	}
	s.pendingActionOrder = order
	return s
}

// confirmPendingActions applies the post-action observation barrier. Lock
// mutations are complete for presentation only when the authoritative
// reservation inventory is known and moved in the expected direction.
func (s appState) confirmPendingActions(ids []string) (appState, []string) {
	confirmed := make([]string, 0, len(ids))
	waiting := make([]string, 0)
	for _, id := range ids {
		action, exists := s.pendingActions[id]
		if !exists {
			continue
		}
		if action.ReservationDelta == 0 {
			confirmed = append(confirmed, id)
			continue
		}
		if !s.reservationsKnown {
			waiting = append(waiting, id)
			continue
		}
		current := s.repoReservations[reservationKey(action.ServerID, action.RepoID)]
		if !action.BaselineReservationsKnown || (action.ReservationDelta > 0 && current > action.BaselineReservations) || (action.ReservationDelta < 0 && current < action.BaselineReservations) {
			confirmed = append(confirmed, id)
		} else {
			waiting = append(waiting, id)
		}
	}
	return s.finishPendingActions(confirmed), waiting
}

func (s appState) projectPendingActions() []PendingAction {
	actions := make([]PendingAction, 0, len(s.pendingActions))
	for _, id := range s.pendingActionOrder {
		if action, exists := s.pendingActions[id]; exists {
			actions = append(actions, action)
		}
	}
	return actions
}

func reservationKey(serverID, repoID string) string { return serverID + "\x00" + repoID }
