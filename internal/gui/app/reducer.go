package app

import (
	"time"

	contract "filees/pkg/contract/v1"
)

// appState is the internal state of the App event loop.
// All methods are pure — they return a new appState and have no side effects.
// Only the event loop goroutine accesses appState; no locking needed.
type appState struct {
	connected               bool
	stale                   bool
	caps                    map[string]bool
	summaries               map[string]contract.RepoSummary // from repo.list; carries URL + LocalPath
	snapshots               map[string]contract.RepoStatus  // from repo.status; carries live state
	order                   []string                        // stable repoID first-seen order
	serverOrder             []string                        // stable serverID first-seen order
	lastSeq                 int64                           // last event sequence number received
	system                  contract.SystemStatusResult
	errors                  []ErrorViewModel
	activity                []ActivityViewModel
	reservations            map[string]int
	repoReservations        map[string]int
	reservationItems        []Reservation
	lockReleaseRequests     []LockReleaseRequest
	serverReservationsKnown map[string]bool
	reservationSources      map[string][]contract.ReservationSource
	notices                 []NoticeViewModel
	publicShares            []PublicShareViewModel
	publicSharesKnown       bool
	pendingActions          map[string]PendingAction
	pendingActionOrder      []string
	refreshed               time.Time
}

func newAppState() appState {
	return appState{
		caps:                    make(map[string]bool),
		summaries:               make(map[string]contract.RepoSummary),
		snapshots:               make(map[string]contract.RepoStatus),
		reservations:            make(map[string]int),
		repoReservations:        make(map[string]int),
		serverReservationsKnown: make(map[string]bool),
		reservationSources:      make(map[string][]contract.ReservationSource),
		pendingActions:          make(map[string]PendingAction),
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
	if !capMap[contract.CapRepoPublicShareList] {
		s.publicShares = nil
		s.publicSharesKnown = false
	}
	if !capMap[contract.CapLockReleaseRequest] {
		s.lockReleaseRequests = nil
	}
	return s
}

// applyFullSnapshot atomically replaces all authoritative daemon/repository
// data and marks it fresh. Removed repositories and their old snapshots are
// pruned as part of the replacement.
func (s appState) applyFullSnapshot(system contract.SystemStatusResult, repos []contract.RepoSummary, statuses []contract.RepoStatus, records []contract.ErrorRecord, activityRecords []contract.ActivityRecord, reservationCounts, repoReservationCounts map[string]int, reservationItems []Reservation, serverReservationsKnown map[string]bool, reservationSources map[string][]contract.ReservationSource, notices []contract.Notice, publicShares []contract.PublicShareSummary, publicSharesKnown bool, refreshed time.Time) appState {
	s = s.applyRepoList(repos)
	next := make(map[string]contract.RepoStatus, len(statuses))
	for _, status := range statuses {
		next[status.RepoID] = status
	}
	s.snapshots = next
	s.system = system
	s.lockReleaseRequests = make([]LockReleaseRequest, 0, len(system.LockReleaseRequests))
	for _, request := range system.LockReleaseRequests {
		s.lockReleaseRequests = append(s.lockReleaseRequests, projectLockReleaseRequest(request))
	}
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
		s.activity = append(s.activity, ActivityViewModel{RepoID: record.RepoID, Path: record.Path, Kind: record.Kind, Stage: record.Stage, UpdatedAt: record.UpdatedAt, Revision: record.Revision, ErrorID: record.ErrorID, Size: record.Size})
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
	s.serverReservationsKnown = make(map[string]bool, len(serverReservationsKnown))
	for serverID, known := range serverReservationsKnown {
		s.serverReservationsKnown[serverID] = known
	}
	s.reservationSources = cloneReservationSources(reservationSources)
	s.notices = make([]NoticeViewModel, 0, len(notices))
	for _, notice := range notices {
		s.notices = append(s.notices, NoticeViewModel{
			ID: notice.ID, RepoID: notice.RepoID, Revision: notice.Revision,
			Title: notice.Title, CreatedAt: notice.CreatedAt, Acked: notice.Acked,
		})
	}
	if publicSharesKnown {
		s.publicShares = make([]PublicShareViewModel, 0, len(publicShares))
		for _, share := range publicShares {
			s.publicShares = append(s.publicShares, PublicShareViewModel{
				ChannelID: share.ChannelID, ServerID: share.ServerID, RepoID: share.RepoID,
				RepoDisplayName: share.RepoDisplayName, Alias: share.Alias, Slug: share.Slug,
				State: share.State, SourceRoot: share.SourceRoot, UpdatedAt: share.UpdatedAt,
				RecipientCount: len(share.Recipients), ObjectCount: len(share.Objects),
				PasswordProtected: share.PasswordProtected, FollowHead: share.DoNotFollow == nil,
			})
		}
		s.publicSharesKnown = true
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
	if ev.Type == contract.EvPublicSharesChanged {
		return s, true, ""
	}
	if ev.Type == contract.EvLockReleaseChanged {
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
			ID:                   id,
			DisplayName:          sum.DisplayName,
			ServerID:             serverID,
			Attached:             sum.Attached,
			Access:               snap.Access,
			OwnerRealmID:         snap.OwnerRealmID,
			AttachmentPolicy:     snap.AttachmentPolicy,
			EditingPolicy:        snap.EditingPolicy,
			Purpose:              firstNonEmpty(snap.Purpose, sum.Purpose),
			URL:                  sum.URL,
			LocalPath:            sum.LocalPath,
			State:                snap.State,
			Connectivity:         snap.Connectivity,
			LocalRev:             snap.LocalRevision,
			HeadRev:              snap.HeadRevision,
			WorkingCopyBytes:     snap.WorkingCopyBytes,
			WorkingCopySizeKnown: snap.WorkingCopySizeKnown,
			Pending:              snap.Pending,
			Conflicts:            snap.Conflicts,
			LastSyncAt:           snap.LastSyncAt,
			CurrentOp:            snap.CurrentOperation,
			ReservationCount:     s.repoReservations[reservationKey(sum.ServerID, sum.ID)],
			Cycle:                snap.Cycle,
			ServerDeleted:        sum.ServerDeleted, LocalCleanupPending: sum.LocalCleanupPending,
			RetainUntil: sum.RetainUntil, RecoveryOperationID: sum.RecoveryOperationID,
			RecoveryAvailable: sum.RecoveryAvailable, RecoveryPending: sum.RecoveryPending, CleanupError: sum.CleanupError,
			LifecycleOperationID: sum.LifecycleOperationID, LifecycleError: sum.LifecycleError,
			CanRetryLifecycle: sum.CanRetryLifecycle, CanAbandonLifecycle: sum.CanAbandonLifecycle,
		})
	}
	serverByID := make(map[string]ServerViewModel, len(s.system.Activations))
	for _, activation := range s.system.Activations {
		projection, asOf := aggregateReservationProjection(s.reservationSources[activation.ServerID], s.serverReservationsKnown[activation.ServerID])
		serverByID[activation.ServerID] = ServerViewModel{ID: activation.ServerID, DisplayName: activation.DisplayName, ClientRole: activation.ClientRole, RealmID: activation.RealmID, RealmAlias: activation.RealmAlias, Address: activation.Address, ClientID: activation.ClientID, SSHPort: activation.SSHPort, CanCreateRepositories: activation.CanCreateRepositories, RepositoriesReady: activation.RepositoriesReady, PendingRequiredRepos: activation.PendingRequiredRepos, SessionTimeoutMin: activation.SessionTimeoutMin, ReservationCount: s.reservations[activation.ServerID], ReservationsKnown: s.serverReservationsKnown[activation.ServerID], ReservationProjection: projection, ReservationAsOf: asOf, ViewGeneration: activation.ViewGeneration, ViewGeneratedAt: activation.ViewGeneratedAt, ViewSyncedAt: activation.ViewSyncedAt, ViewSyncError: activation.ViewSyncError, ViewSyncFailures: activation.ViewSyncFailures, ServerViewProducedAt: activation.ServerViewProducedAt, Detached: activation.Detached}
	}
	for _, repo := range repos {
		server, ok := serverByID[repo.ServerID]
		if !ok {
			projection, asOf := aggregateReservationProjection(s.reservationSources[repo.ServerID], s.serverReservationsKnown[repo.ServerID])
			server = ServerViewModel{ID: repo.ServerID, DisplayName: repo.ServerID, ReservationCount: s.reservations[repo.ServerID], ReservationsKnown: s.serverReservationsKnown[repo.ServerID], ReservationProjection: projection, ReservationAsOf: asOf}
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
	// A detached server leaves the view entirely, and so do its repositories.
	//
	// Detaching is something the owner does deliberately, confirming three
	// times, and afterwards there is nothing to be done about it from here.
	// Keeping the server on screen is therefore not information; it is the name
	// of a server somebody has just cut ties with, next to the names of its
	// repositories, sitting where anyone can read them. The owner named the
	// case plainly: a server whose subject matter he would rather not have on
	// display with people around. That makes this an exposure rather than
	// untidiness, and only absence fixes it.
	//
	// A "recently detached" panel with a lifetime of about forty-eight hours is
	// the agreed way back to this information for whoever wants it. It is not
	// built yet, and it must not be approximated by leaving everything visible
	// in the meantime.
	servers = withoutDetached(servers)
	repos = reposOfListedServers(repos, servers)
	caps := make(map[string]bool, len(s.caps))
	for k, v := range s.caps {
		caps[k] = v
	}
	vm := ViewModel{
		Connected:           s.connected,
		Stale:               s.stale,
		DaemonState:         s.system.State,
		UptimeSec:           s.system.UptimeSec,
		LastRefresh:         s.refreshed,
		Capabilities:        caps,
		Repos:               repos,
		Servers:             servers,
		Reservations:        append([]Reservation(nil), s.reservationItems...),
		LockReleaseRequests: append([]LockReleaseRequest(nil), s.lockReleaseRequests...),
		Errors:              append([]ErrorViewModel(nil), s.errors...),
		Activity:            append([]ActivityViewModel(nil), s.activity...),
		Notices:             append([]NoticeViewModel(nil), s.notices...),
		PublicShares:        append([]PublicShareViewModel(nil), s.publicShares...),
		PublicSharesKnown:   s.publicSharesKnown,
		PendingActions:      s.projectPendingActions(),
	}
	// Detachments pass through unfiltered. The daemon owns the lifetime and
	// has already applied it; recomputing it here would put a second opinion
	// about time into the presentation layer, and the two would disagree the
	// moment one of them was wrong.
	for _, record := range s.system.Detachments {
		vm.Detachments = append(vm.Detachments, DetachmentViewModel{
			ServerID: record.ServerID, DisplayName: record.DisplayName, Address: record.Address,
			Cause: record.Cause, At: record.At, ReattachedAt: record.ReattachedAt, WorkingCopies: record.WorkingCopies,
		})
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
			State: update.State, Channel: update.Channel, CurrentVersion: update.CurrentVersion,
			AvailableVersion: update.AvailableVersion, ReleaseID: update.ReleaseID,
			Summary: update.Summary, RestartRequired: update.RestartRequired,
		}
	}
	if s.connected && s.stale {
		vm.Icon = IconBusy
	} else {
		unread := 0
		for _, notice := range s.notices {
			if !notice.Acked {
				unread++
			}
		}
		vm.Icon = aggregateIcon(s.connected, repos, unread)
	}
	return vm
}

func cloneReservationSources(source map[string][]contract.ReservationSource) map[string][]contract.ReservationSource {
	result := make(map[string][]contract.ReservationSource, len(source))
	for serverID, rows := range source {
		result[serverID] = append([]contract.ReservationSource(nil), rows...)
	}
	return result
}

func aggregateReservationProjection(sources []contract.ReservationSource, known bool) (string, string) {
	if !known {
		return string(contract.ReservationSourceUnknown), ""
	}
	// A source is "unknown" both when the server could not answer for it and
	// when it was never asked at all: the coordinator only queries
	// repositories in state "active" (cmd/filees/reservation_projection.go).
	// Letting one unknown decide the whole server therefore marked every
	// server as having no current emission for as long as it held a single
	// inactive repository - permanently, on any real installation, while
	// emissions were in fact arriving. The wire contract deliberately carries
	// no server-level aggregate for exactly this reason; this function is the
	// one place that reintroduces one, so it must do so honestly.
	//
	// Unknown therefore wins only when nothing at all could be obtained. When
	// at least one source answered, the server does have an emission and the
	// aggregate is the worst of the answered ones; partial coverage is
	// reported separately by the caller.
	state := contract.ReservationSourceFresh
	answered := false
	var oldest time.Time
	for _, source := range sources {
		switch source.State {
		case contract.ReservationSourceUnknown:
			continue
		case contract.ReservationSourceOffline:
			state = contract.ReservationSourceOffline
		case contract.ReservationSourceStale:
			if state == contract.ReservationSourceFresh {
				state = contract.ReservationSourceStale
			}
		}
		answered = true
		if !source.AsOf.IsZero() && (oldest.IsZero() || source.AsOf.Before(oldest)) {
			oldest = source.AsOf
		}
	}
	if !answered {
		return string(contract.ReservationSourceUnknown), ""
	}
	if oldest.IsZero() {
		return string(state), ""
	}
	return string(state), oldest.UTC().Format(time.RFC3339Nano)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
	// A successful lock mutation cannot be fenced against an inventory that
	// was already unavailable before the operation began.  The daemon call is
	// still authoritative for success/failure; only the optional presentation
	// confirmation is skipped.  Otherwise the spinner can wait forever for a
	// reservation snapshot the daemon has explicitly reported as unknown.
	if action.ReservationDelta != 0 && !action.BaselineReservationsKnown {
		return s.finishPendingActions([]string{id})
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

// confirmPendingActions applies the post-action observation barrier. A mutation
// is complete for presentation only when the authoritative projection shows its
// expected effect (timeout, repository lifecycle state or reservation delta).
func (s appState) confirmPendingActions(ids []string) (appState, []string) {
	confirmed := make([]string, 0, len(ids))
	waiting := make([]string, 0)
	for _, id := range ids {
		action, exists := s.pendingActions[id]
		if !exists {
			continue
		}
		if action.ExpectedSessionTimeoutMin > 0 {
			matched := false
			for _, activation := range s.system.Activations {
				if activation.ServerID == action.ServerID && activation.SessionTimeoutMin == action.ExpectedSessionTimeoutMin {
					matched = true
					break
				}
			}
			if matched {
				confirmed = append(confirmed, id)
			} else {
				waiting = append(waiting, id)
			}
			continue
		}
		if action.ExpectedRepoDeleted {
			summary, exists := s.summaries[action.RepoID]
			if !exists || summary.ServerID != action.ServerID || summary.ServerDeleted {
				confirmed = append(confirmed, id)
			} else {
				waiting = append(waiting, id)
			}
			continue
		}
		if action.ExpectedLifecycleOperationID != "" {
			summary, exists := s.summaries[action.RepoID]
			if !exists || summary.ServerID != action.ServerID || summary.LifecycleOperationID != action.ExpectedLifecycleOperationID {
				confirmed = append(confirmed, id)
			} else if summary.LifecycleError != "" {
				// Repair actions enter the reducer only after the daemon has
				// acknowledged them and cleared the old error behind the exact
				// operation fence. Seeing an error here therefore means the
				// resumed worker reached a new actionable failure.
				confirmed = append(confirmed, id)
			} else {
				waiting = append(waiting, id)
			}
			continue
		}
		if action.ExpectedRepoAttached {
			summary, exists := s.summaries[action.RepoID]
			if exists && summary.ServerID == action.ServerID && summary.Attached {
				confirmed = append(confirmed, id)
			} else {
				waiting = append(waiting, id)
			}
			continue
		}
		if action.ExpectedRepoDetached {
			summary, exists := s.summaries[action.RepoID]
			if exists && summary.ServerID == action.ServerID && !summary.Attached {
				confirmed = append(confirmed, id)
			} else {
				waiting = append(waiting, id)
			}
			continue
		}
		if action.ReservationDelta == 0 {
			confirmed = append(confirmed, id)
			continue
		}
		if !s.serverReservationsKnown[action.ServerID] {
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

func withoutDetached(servers []ServerViewModel) []ServerViewModel {
	kept := make([]ServerViewModel, 0, len(servers))
	for _, server := range servers {
		if server.Detached {
			continue
		}
		kept = append(kept, server)
	}
	return kept
}

// reposOfListedServers drops the repositories of servers that are no longer
// listed. Without it the server heading disappears while its folders remain in
// every count and every flat list - which would expose exactly what removing
// the server was meant to withhold, and confuse the totals besides.
func reposOfListedServers(repos []RepoViewModel, servers []ServerViewModel) []RepoViewModel {
	listed := make(map[string]bool, len(servers))
	for _, server := range servers {
		listed[server.ID] = true
	}
	kept := make([]RepoViewModel, 0, len(repos))
	for _, repo := range repos {
		if repo.ServerID != "" && !listed[repo.ServerID] {
			continue
		}
		kept = append(kept, repo)
	}
	return kept
}
