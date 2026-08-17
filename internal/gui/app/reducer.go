package app

import (
	"time"

	contract "filees/pkg/contract/v1"
)

// appState is the internal state of the App event loop.
// All methods are pure — they return a new appState and have no side effects.
// Only the event loop goroutine accesses appState; no locking needed.
type appState struct {
	connected         bool
	stale             bool
	caps              map[string]bool
	summaries         map[string]contract.RepoSummary // from repo.list; carries URL + LocalPath
	snapshots         map[string]contract.RepoStatus  // from repo.status; carries live state
	order             []string                        // repoID insertion order from repo.list
	lastSeq           int64                           // last event sequence number received
	system            contract.SystemStatusResult
	errors            []ErrorViewModel
	activity          []ActivityViewModel
	reservations      map[string]int
	repoReservations  map[string]int
	reservationsKnown bool
	notices           []NoticeViewModel
	refreshed         time.Time
}

func newAppState() appState {
	return appState{
		caps:             make(map[string]bool),
		summaries:        make(map[string]contract.RepoSummary),
		snapshots:        make(map[string]contract.RepoStatus),
		reservations:     make(map[string]int),
		repoReservations: make(map[string]int),
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
func (s appState) applyFullSnapshot(system contract.SystemStatusResult, repos []contract.RepoSummary, statuses []contract.RepoStatus, records []contract.ErrorRecord, activityRecords []contract.ActivityRecord, reservationCounts, repoReservationCounts map[string]int, reservationsKnown bool, notices []contract.Notice, refreshed time.Time) appState {
	s = s.applyRepoList(repos)
	next := make(map[string]contract.RepoStatus, len(statuses))
	for _, status := range statuses {
		next[status.RepoID] = status
	}
	s.snapshots = next
	s.system = system
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
		sum := s.summaries[id]
		snap := s.snapshots[id]
		repos = append(repos, RepoViewModel{
			ID:               id,
			DisplayName:      sum.DisplayName,
			ServerID:         snap.ServerID,
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
		})
	}
	servers := make([]ServerViewModel, 0, len(s.system.Activations))
	byServer := make(map[string]int, len(s.system.Activations))
	for _, activation := range s.system.Activations {
		byServer[activation.ServerID] = len(servers)
		servers = append(servers, ServerViewModel{ID: activation.ServerID, DisplayName: activation.DisplayName, ClientRole: activation.ClientRole, RealmID: activation.RealmID, RealmAlias: activation.RealmAlias, Address: activation.Address, ClientID: activation.ClientID, SSHPort: activation.SSHPort, CanCreateRepositories: activation.CanCreateRepositories, RepositoriesReady: activation.RepositoriesReady, PendingRequiredRepos: activation.PendingRequiredRepos, SessionTimeoutMin: activation.SessionTimeoutMin, ReservationCount: s.reservations[activation.ServerID], ReservationsKnown: s.reservationsKnown})
	}
	for _, repo := range repos {
		index, ok := byServer[repo.ServerID]
		if !ok {
			byServer[repo.ServerID] = len(servers)
			servers = append(servers, ServerViewModel{ID: repo.ServerID, DisplayName: repo.ServerID, ReservationCount: s.reservations[repo.ServerID], ReservationsKnown: s.reservationsKnown})
			index = len(servers) - 1
		}
		servers[index].Repos = append(servers[index].Repos, repo)
	}
	caps := make(map[string]bool, len(s.caps))
	for k, v := range s.caps {
		caps[k] = v
	}
	vm := ViewModel{
		Connected:    s.connected,
		Stale:        s.stale,
		DaemonState:  s.system.State,
		UptimeSec:    s.system.UptimeSec,
		LastRefresh:  s.refreshed,
		Capabilities: caps,
		Repos:        repos,
		Servers:      servers,
		Errors:       append([]ErrorViewModel(nil), s.errors...),
		Activity:     append([]ActivityViewModel(nil), s.activity...),
		Notices:      append([]NoticeViewModel(nil), s.notices...),
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

func reservationKey(serverID, repoID string) string { return serverID + "\x00" + repoID }
