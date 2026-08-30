package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/journal"
	"filees/internal/gui/tray"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
)

const (
	snapshotEvent       = "filees:snapshot"
	actionFeedbackEvent = "filees:action-feedback"
)

type snapshotEmitter interface {
	Emit(name string, data ...any) bool
}

// GUIService is the entire Wails-to-FileES bridge.  It owns no domain state:
// internal/gui/app reconstructs the authoritative presentation from IPC and
// this service only publishes an immutable browser-friendly projection.
type GUIService struct {
	mu        sync.RWMutex
	snapshot  Snapshot
	view      guiapp.ViewModel
	runner    *guiapp.App
	emitter   snapshotEmitter
	actions   chan<- tray.Intent
	actionSeq atomic.Uint64
	observer  func(Snapshot)
}

type Snapshot struct {
	Revision          uint64                           `json:"revision"`
	Connected         bool                             `json:"connected"`
	Stale             bool                             `json:"stale"`
	DaemonState       string                           `json:"daemon_state"`
	UptimeSec         int64                            `json:"uptime_sec"`
	LastRefresh       string                           `json:"last_refresh,omitempty"`
	IconState         string                           `json:"icon_state"`
	Capabilities      []string                         `json:"capabilities"`
	Servers           []ServerProjection               `json:"servers"`
	Repositories      []RepoProjection                 `json:"repositories"`
	Reservations      []ReservationProjection          `json:"reservations"`
	Errors            []ErrorProjection                `json:"errors"`
	Activity          []ActivityProjection             `json:"activity"`
	Journal           []JournalProjection              `json:"journal"`
	PendingActions    []PendingActionProjection        `json:"pending_actions"`
	NextCycleAt       string                           `json:"next_cycle_at,omitempty"`
	CycleRunning      bool                             `json:"cycle_running"`
	Notices           []NoticeProjection               `json:"notices"`
	PublicShares      []DashboardPublicShareProjection `json:"public_shares"`
	PublicSharesKnown bool                             `json:"public_shares_known"`
	Update            *UpdateProjection                `json:"update,omitempty"`
	ClientVersion     string                           `json:"client_version"`
}

type ServerProjection struct {
	ID                    string `json:"id"`
	DisplayName           string `json:"display_name"`
	Address               string `json:"address"`
	ClientRole            string `json:"client_role"`
	RealmAlias            string `json:"realm_alias,omitempty"`
	RepositoryCount       int    `json:"repository_count"`
	RepositoriesReady     bool   `json:"repositories_ready"`
	PendingRequiredRepos  int    `json:"pending_required_repos"`
	ReservationCount      int    `json:"reservation_count"`
	ReservationsKnown     bool   `json:"reservations_known"`
	SessionTimeoutMinutes int    `json:"session_timeout_minutes"`
}

type RepoProjection struct {
	ID                   string          `json:"id"`
	ServerID             string          `json:"server_id"`
	DisplayName          string          `json:"display_name"`
	LocalPath            string          `json:"local_path,omitempty"`
	URL                  string          `json:"url,omitempty"`
	Attached             bool            `json:"attached"`
	Access               string          `json:"access"`
	Ownership            string          `json:"ownership"`
	AttachmentPolicy     string          `json:"attachment_policy"`
	State                string          `json:"state"`
	DisplayState         string          `json:"display_state"`
	Connectivity         string          `json:"connectivity"`
	LocalRevision        int64           `json:"local_revision"`
	HeadRevision         int64           `json:"head_revision"`
	WorkingCopyBytes     int64           `json:"working_copy_bytes,omitempty"`
	WorkingCopySizeKnown bool            `json:"working_copy_size_known,omitempty"`
	PendingFiles         int             `json:"pending_files"`
	PendingBytes         int64           `json:"pending_bytes"`
	Conflicts            int             `json:"conflicts"`
	CurrentOperation     string          `json:"current_operation,omitempty"`
	ReservationCount     int             `json:"reservation_count"`
	CanOpen              bool            `json:"can_open"`
	CanLock              bool            `json:"can_lock"`
	CanUnlock            bool            `json:"can_unlock"`
	CanPublish           bool            `json:"can_publish"`
	CanReviewQuarantine  bool            `json:"can_review_quarantine"`
	Cycle                CycleProjection `json:"cycle"`
	ServerDeleted        bool            `json:"server_deleted,omitempty"`
	LocalCleanupPending  bool            `json:"local_cleanup_pending,omitempty"`
	RetainUntil          string          `json:"retain_until,omitempty"`
	RecoveryOperationID  string          `json:"recovery_operation_id,omitempty"`
	RecoveryAvailable    bool            `json:"recovery_available,omitempty"`
	RecoveryPending      bool            `json:"recovery_pending,omitempty"`
	CleanupError         string          `json:"cleanup_error,omitempty"`
	Purpose              string          `json:"purpose,omitempty"`
}

type CycleProjection struct {
	ID         uint64 `json:"cycle_id"`
	Phase      string `json:"phase,omitempty"`
	LastTickAt string `json:"last_tick_at,omitempty"`
	NextTickAt string `json:"next_tick_at,omitempty"`
}

type PendingActionProjection struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	RepoID    string `json:"repo_id,omitempty"`
	ServerID  string `json:"server_id,omitempty"`
	Label     string `json:"label"`
	Phase     string `json:"phase"`
	StartedAt string `json:"started_at"`
}

type ReservationProjection struct {
	ID             string `json:"id"`
	ServerID       string `json:"server_id"`
	RepoID         string `json:"repo_id"`
	Repository     string `json:"repository"`
	Path           string `json:"path"`
	OwnerLabel     string `json:"owner_label,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	CanRelease     bool   `json:"can_release"`
	LocalChanges   bool   `json:"local_changes"`
	ActivePassport bool   `json:"active_passport"`
}

type ActionRequest struct {
	Kind          string   `json:"kind"`
	RepoID        string   `json:"repo_id,omitempty"`
	ServerID      string   `json:"server_id,omitempty"`
	ReservationID string   `json:"reservation_id,omitempty"`
	NoticeID      string   `json:"notice_id,omitempty"`
	ChannelID     string   `json:"channel_id,omitempty"`
	ChannelIDs    []string `json:"channel_ids,omitempty"`
}

type ActionAcceptance struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
}

type ActionFeedback struct {
	Level   string `json:"level"`
	Title   string `json:"title"`
	Message string `json:"message,omitempty"`
}

type ErrorProjection struct {
	ID        string `json:"id"`
	RepoID    string `json:"repo_id,omitempty"`
	Timestamp string `json:"timestamp"`
	Code      string `json:"code"`
	Severity  string `json:"severity"`
	Hint      string `json:"hint,omitempty"`
	Message   string `json:"message"`
}

type ActivityProjection struct {
	RepoID   string `json:"repo_id"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Stage    string `json:"stage"`
	Updated  string `json:"updated_at"`
	Revision int64  `json:"revision,omitempty"`
	ErrorID  string `json:"error_id,omitempty"`
	Size     *int64 `json:"size,omitempty"`
}

type JournalProjection struct {
	ID           string `json:"id"`
	RelativeTime string `json:"relative_time"`
	ExactTime    string `json:"exact_time"`
	Repository   string `json:"repository"`
	Summary      string `json:"summary"`
	Details      string `json:"details,omitempty"`
	Severity     string `json:"severity,omitempty"`
	Emphasized   bool   `json:"emphasized"`
}

type NoticeProjection struct {
	ID        string `json:"id"`
	RepoID    string `json:"repo_id,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	Acked     bool   `json:"acked"`
	CanAck    bool   `json:"can_ack"`
}

type DashboardPublicShareProjection struct {
	ChannelID         string `json:"channel_id"`
	ServerID          string `json:"server_id"`
	RepoID            string `json:"repo_id"`
	Repository        string `json:"repository"`
	Address           string `json:"address"`
	State             string `json:"state"`
	SourceRoot        string `json:"source_root,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
	RecipientCount    int    `json:"recipient_count"`
	ObjectCount       int    `json:"object_count"`
	PasswordProtected bool   `json:"password_protected"`
	FollowHead        bool   `json:"follow_head"`
	CanOpen           bool   `json:"can_open"`
	CanRevoke         bool   `json:"can_revoke"`
}

type UpdateProjection struct {
	State            string `json:"state"`
	CurrentVersion   string `json:"current_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	Summary          string `json:"summary,omitempty"`
	RestartRequired  bool   `json:"restart_required"`
}

func newGUIService(client guiapp.DaemonClient) *GUIService {
	service := &GUIService{
		snapshot: Snapshot{
			Stale:         true,
			IconState:     string(guiapp.IconDisconnected),
			Capabilities:  []string{},
			Servers:       []ServerProjection{},
			Repositories:  []RepoProjection{},
			Errors:        []ErrorProjection{},
			Activity:      []ActivityProjection{},
			Journal:       []JournalProjection{},
			Notices:       []NoticeProjection{},
			PublicShares:  []DashboardPublicShareProjection{},
			ClientVersion: version,
		},
	}
	service.runner = guiapp.New(guiapp.Config{
		Client:   client,
		OnChange: service.onChange,
	})
	return service
}

func (service *GUIService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

func (service *GUIService) attachActions(actions chan<- tray.Intent) {
	service.mu.Lock()
	service.actions = actions
	service.mu.Unlock()
}

func (service *GUIService) attachSnapshotObserver(observer func(Snapshot)) {
	service.mu.Lock()
	service.observer = observer
	current := service.snapshot
	service.mu.Unlock()
	if observer != nil {
		observer(current)
	}
}

func (service *GUIService) run(ctx context.Context) {
	service.runner.Run(ctx)
}

// Snapshot returns what the IPC presentation model currently knows.  It never
// reads the filesystem, repository metadata or server configuration itself.
func (service *GUIService) Snapshot() Snapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.snapshot
}

// Trigger translates a closed set of browser gestures into the same intents
// consumed by the Fyne composition root. Acceptance means queued, never that
// the operation succeeded; the controller and daemon still decide the result.
func (service *GUIService) Trigger(request ActionRequest) ActionAcceptance {
	request.Kind = strings.TrimSpace(request.Kind)
	request.RepoID = strings.TrimSpace(request.RepoID)
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.ReservationID = strings.TrimSpace(request.ReservationID)
	request.NoticeID = strings.TrimSpace(request.NoticeID)
	request.ChannelID = strings.TrimSpace(request.ChannelID)
	request.ChannelIDs = cleanUniqueStrings(request.ChannelIDs)

	service.mu.RLock()
	vm := service.view
	actions := service.actions
	service.mu.RUnlock()
	if actions == nil {
		return ActionAcceptance{Code: "actions_unavailable"}
	}
	intent, allowed := translateAction(vm, request)
	if !allowed {
		return ActionAcceptance{Code: "action_unavailable"}
	}
	tracked := pendingActionFor(vm, request, service.actionSeq.Add(1))
	if tracked.ID != "" && service.runner != nil {
		if !service.runner.StartAction(tracked) {
			return ActionAcceptance{Code: "action_queue_busy"}
		}
		intent.ActionID = tracked.ID
	}
	select {
	case actions <- intent:
		return ActionAcceptance{Accepted: true}
	default:
		if tracked.ID != "" && service.runner != nil {
			service.runner.FinishAction(tracked.ID)
		}
		return ActionAcceptance{Code: "action_queue_busy"}
	}
}

func pendingActionFor(vm guiapp.ViewModel, request ActionRequest, sequence uint64) guiapp.PendingAction {
	action := guiapp.PendingAction{Kind: request.Kind, StartedAt: time.Now()}
	switch request.Kind {
	case string(tray.IntentLock):
		action.Label = "Zakładanie blokady"
		action.RepoID = request.RepoID
		action.ReservationDelta = 1
	case string(tray.IntentUnlock):
		action.Label = "Zwalnianie blokady"
		action.RepoID = request.RepoID
		action.ReservationDelta = -1
	case string(tray.IntentReleaseReservation):
		action.Label = "Zwalnianie blokady"
		action.ReservationDelta = -1
		if reservation, ok := projectedReservation(vm, request.ReservationID); ok {
			action.RepoID = reservation.RepoID
			action.ServerID = reservation.ServerID
		}
	default:
		return guiapp.PendingAction{}
	}
	if action.ServerID == "" && action.RepoID != "" {
		if repo, ok := projectedRepo(vm, action.RepoID); ok {
			action.ServerID = repo.ServerID
			action.BaselineReservations = repo.ReservationCount
		}
	}
	if action.RepoID != "" && action.BaselineReservations == 0 {
		if repo, ok := projectedRepo(vm, action.RepoID); ok {
			action.BaselineReservations = repo.ReservationCount
		}
	}
	for _, server := range vm.Servers {
		if server.ID == action.ServerID {
			action.BaselineReservationsKnown = server.ReservationsKnown
			break
		}
	}
	action.ID = request.Kind + ":" + fmt.Sprint(sequence)
	return action
}

// Refresh is a presentation intent: ask the shared IPC model to fetch a fresh
// authoritative snapshot without tearing down its event subscription.
func (service *GUIService) Refresh() {
	service.runner.Refresh()
}

// Reconnect is a presentation intent used by the offline screen.
func (service *GUIService) Reconnect() {
	service.runner.Reconnect()
}

func (service *GUIService) onChange(vm guiapp.ViewModel) {
	next := projectViewModel(vm)

	service.mu.Lock()
	next.Revision = service.snapshot.Revision + 1
	service.snapshot = next
	service.view = vm
	emitter := service.emitter
	observer := service.observer
	service.mu.Unlock()

	if emitter != nil {
		emitter.Emit(snapshotEvent, next)
	}
	if observer != nil {
		observer(next)
	}
}

func (service *GUIService) viewModel() guiapp.ViewModel {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.view
}

func (service *GUIService) emitActionFeedback(feedback ActionFeedback) {
	service.mu.RLock()
	emitter := service.emitter
	service.mu.RUnlock()
	if emitter != nil {
		emitter.Emit(actionFeedbackEvent, feedback)
	}
}

func translateAction(vm guiapp.ViewModel, request ActionRequest) (tray.Intent, bool) {
	switch request.Kind {
	case string(tray.IntentActivate):
		return tray.Intent{Kind: tray.IntentActivate}, true
	case string(tray.IntentPairMobileDevice):
		if !vm.CanPairMobile() || len(vm.Servers) == 0 {
			return tray.Intent{}, false
		}
		return tray.Intent{Kind: tray.IntentPairMobileDevice, ServerID: vm.Servers[0].ID}, true
	case string(tray.IntentRestartFileES):
		return tray.Intent{Kind: tray.IntentRestartFileES}, vm.CanRestartFileES()
	case string(tray.IntentShutdownFileES):
		return tray.Intent{Kind: tray.IntentShutdownFileES}, vm.CanShutdownFileES()
	case string(tray.IntentUpdatePlan):
		return tray.Intent{Kind: tray.IntentUpdatePlan}, vm.CanPlanUpdate()
	case string(tray.IntentUpdateApply):
		return tray.Intent{Kind: tray.IntentUpdateApply}, vm.CanApplyUpdate()
	case string(tray.IntentReleaseReservation):
		reservation, ok := projectedReservation(vm, request.ReservationID)
		allowed := ok && reservation.CanRelease && vm.CanReleaseReservations() && viewHasServer(vm, reservation.ServerID)
		return tray.Intent{Kind: tray.IntentReleaseReservation, ReservationID: request.ReservationID}, allowed
	case string(tray.IntentAckNotice):
		allowed := vm.Connected && !vm.Stale && vm.CanAckNotices() && projectedNotice(vm, request.NoticeID)
		return tray.Intent{Kind: tray.IntentAckNotice, NoticeID: request.NoticeID}, allowed
	case string(tray.IntentManagePublicShares), string(tray.IntentRevokePublicShare):
		share, ok := projectedPublicShare(vm, request.ServerID, request.RepoID, request.ChannelID)
		if !ok || !vm.CanManagePublicShares() {
			return tray.Intent{}, false
		}
		kind := tray.IntentManagePublicShares
		allowed := true
		if request.Kind == string(tray.IntentRevokePublicShare) {
			kind = tray.IntentRevokePublicShare
			allowed = strings.EqualFold(strings.TrimSpace(share.State), "active")
		}
		return tray.Intent{Kind: kind, ServerID: share.ServerID, RepoID: share.RepoID, ChannelID: share.ChannelID}, allowed
	case string(tray.IntentRevokePublicShares):
		channelIDs := cleanUniqueStrings(request.ChannelIDs)
		if !vm.CanManagePublicShares() || request.ServerID == "" || len(channelIDs) == 0 {
			return tray.Intent{}, false
		}
		for _, channelID := range channelIDs {
			share, ok := projectedPublicShareByChannel(vm, channelID)
			if !ok || share.ServerID != request.ServerID || !strings.EqualFold(strings.TrimSpace(share.State), "active") {
				return tray.Intent{}, false
			}
		}
		return tray.Intent{Kind: tray.IntentRevokePublicShares, ServerID: request.ServerID, ChannelIDs: channelIDs}, true
	case string(tray.IntentSettings):
		intent := tray.Intent{Kind: tray.IntentSettings, ServerID: request.ServerID, RepoID: request.RepoID}
		if request.RepoID == "" {
			return intent, viewHasServer(vm, request.ServerID)
		}
		repo, ok := projectedRepo(vm, request.RepoID)
		return intent, ok && repo.ServerID == request.ServerID && viewHasServer(vm, request.ServerID)
	}
	repo, ok := projectedRepo(vm, request.RepoID)
	if !ok {
		return tray.Intent{}, false
	}
	switch request.Kind {
	case string(tray.IntentDownloadRecovery):
		allowed := repo.ServerDeleted && repo.RecoveryAvailable && repo.RecoveryOperationID != ""
		return tray.Intent{Kind: tray.IntentDownloadRecovery, RepoID: repo.ID, ServerID: repo.ServerID, RecoveryOperationID: repo.RecoveryOperationID}, allowed
	case string(tray.IntentOpenFolder):
		return tray.Intent{Kind: tray.IntentOpenFolder, RepoID: repo.ID}, repo.Attached && strings.TrimSpace(repo.LocalPath) != ""
	case string(tray.IntentAttachRepository):
		allowed := !repo.Attached && repo.DisplayState() == guiapp.RepoDisplayUnattached && vm.CanAttachRepository()
		return tray.Intent{Kind: tray.IntentAttachRepository, RepoID: repo.ID, ServerID: repo.ServerID}, allowed
	case string(tray.IntentLock):
		allowed := vm.CanMutateLock() && repo.Attached && repo.CanWrite() && strings.TrimSpace(repo.LocalPath) != "" && serverAllowsLock(vm, repo.ServerID)
		return tray.Intent{Kind: tray.IntentLock, RepoID: repo.ID}, allowed
	case string(tray.IntentUnlock):
		allowed := vm.CanMutateUnlock() && repo.Attached && repo.CanWrite() && strings.TrimSpace(repo.LocalPath) != "" && repo.ReservationCount > 0
		return tray.Intent{Kind: tray.IntentUnlock, RepoID: repo.ID}, allowed
	case string(tray.IntentPublish):
		allowed := vm.Connected && !vm.Stale && vm.CanPublish() && repo.Attached && repo.CanWrite() && strings.TrimSpace(repo.LocalPath) != ""
		return tray.Intent{Kind: tray.IntentPublish, RepoID: repo.ID}, allowed
	case string(tray.IntentReviewQuarantine):
		allowed := vm.CanReviewQuarantine() && repo.Purpose == clientview.PurposeUploadTrash && serverOwnsRepo(vm, repo)
		return tray.Intent{Kind: tray.IntentReviewQuarantine, RepoID: repo.ID, ServerID: repo.ServerID}, allowed
	default:
		return tray.Intent{}, false
	}
}

func projectedNotice(vm guiapp.ViewModel, noticeID string) bool {
	for _, notice := range vm.Notices {
		if notice.ID == noticeID && !notice.Acked {
			return true
		}
	}
	return false
}

func projectedPublicShare(vm guiapp.ViewModel, serverID, repoID, channelID string) (guiapp.PublicShareViewModel, bool) {
	for _, share := range vm.PublicShares {
		if share.ServerID == serverID && share.RepoID == repoID && share.ChannelID == channelID {
			return share, true
		}
	}
	return guiapp.PublicShareViewModel{}, false
}

func projectedPublicShareByChannel(vm guiapp.ViewModel, channelID string) (guiapp.PublicShareViewModel, bool) {
	for _, share := range vm.PublicShares {
		if share.ChannelID == channelID && channelID != "" {
			return share, true
		}
	}
	return guiapp.PublicShareViewModel{}, false
}

func cleanUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func projectedReservation(vm guiapp.ViewModel, reservationID string) (guiapp.Reservation, bool) {
	for _, reservation := range vm.Reservations {
		if reservation.ID == reservationID {
			return reservation, true
		}
	}
	return guiapp.Reservation{}, false
}

func viewHasServer(vm guiapp.ViewModel, serverID string) bool {
	for _, server := range vm.Servers {
		if server.ID == serverID {
			return true
		}
	}
	return false
}

func serverOwnsRepo(vm guiapp.ViewModel, repo guiapp.RepoViewModel) bool {
	for _, server := range vm.Servers {
		if server.ID == repo.ServerID {
			return server.Owns(repo)
		}
	}
	return false
}

func projectedRepo(vm guiapp.ViewModel, repoID string) (guiapp.RepoViewModel, bool) {
	for _, repo := range vm.Repos {
		if repo.ID == repoID {
			return repo, true
		}
	}
	return guiapp.RepoViewModel{}, false
}

func serverAllowsLock(vm guiapp.ViewModel, serverID string) bool {
	for _, server := range vm.Servers {
		if server.ID == serverID {
			return server.RealmID == "" || server.RealmAlias != ""
		}
	}
	return true
}

func projectViewModel(vm guiapp.ViewModel) Snapshot {
	return projectViewModelAt(vm, time.Now())
}

func projectViewModelAt(vm guiapp.ViewModel, now time.Time) Snapshot {
	result := Snapshot{
		Connected:      vm.Connected,
		Stale:          vm.Stale,
		DaemonState:    vm.DaemonState,
		UptimeSec:      vm.UptimeSec,
		IconState:      string(vm.Icon),
		Capabilities:   make([]string, 0, len(vm.Capabilities)),
		Servers:        make([]ServerProjection, 0, len(vm.Servers)),
		Repositories:   make([]RepoProjection, 0, len(vm.Repos)),
		Reservations:   make([]ReservationProjection, 0, len(vm.Reservations)),
		Errors:         make([]ErrorProjection, 0, len(vm.Errors)),
		Activity:       make([]ActivityProjection, 0, len(vm.Activity)),
		Journal:        []JournalProjection{},
		PendingActions: make([]PendingActionProjection, 0, len(vm.PendingActions)),
		Notices:        make([]NoticeProjection, 0, len(vm.Notices)),
		ClientVersion:  version,
	}
	if !vm.LastRefresh.IsZero() {
		result.LastRefresh = vm.LastRefresh.Format(time.RFC3339)
	}
	for capability, enabled := range vm.Capabilities {
		if enabled {
			result.Capabilities = append(result.Capabilities, capability)
		}
	}
	sort.Strings(result.Capabilities)

	for _, server := range vm.Servers {
		result.Servers = append(result.Servers, ServerProjection{
			ID: server.ID, DisplayName: server.DisplayName, Address: server.Address,
			ClientRole: server.ClientRole, RealmAlias: server.RealmAlias,
			RepositoryCount: len(server.Repos), RepositoriesReady: server.RepositoriesReady,
			PendingRequiredRepos: server.PendingRequiredRepos,
			ReservationCount:     server.ReservationCount, ReservationsKnown: server.ReservationsKnown,
			SessionTimeoutMinutes: server.SessionTimeoutMin,
		})
	}
	serversByID := make(map[string]guiapp.ServerViewModel, len(vm.Servers))
	for _, server := range vm.Servers {
		serversByID[server.ID] = server
	}
	for _, repo := range vm.Repos {
		operation := ""
		if repo.CurrentOp != nil {
			operation = *repo.CurrentOp
		}
		canOpen := repo.Attached && strings.TrimSpace(repo.LocalPath) != ""
		canLock := vm.CanMutateLock() && canOpen && repo.CanWrite() && serverAllowsLock(vm, repo.ServerID)
		canUnlock := vm.CanMutateUnlock() && canOpen && repo.CanWrite() && repo.ReservationCount > 0
		canPublish := vm.Connected && !vm.Stale && vm.CanPublish() && canOpen && repo.CanWrite()
		canReviewQuarantine := false
		ownership := "unclassified"
		if server, ok := serversByID[repo.ServerID]; ok && server.RealmID != "" && repo.OwnerRealmID != "" {
			if server.Owns(repo) {
				ownership = "owned"
				canReviewQuarantine = vm.CanReviewQuarantine() && repo.Purpose == clientview.PurposeUploadTrash
			} else {
				ownership = "guest"
			}
		}
		result.Repositories = append(result.Repositories, RepoProjection{
			ID: repo.ID, ServerID: repo.ServerID, DisplayName: repo.DisplayName,
			LocalPath: repo.LocalPath, URL: repo.URL, Attached: repo.Attached,
			Access: repo.Access, Ownership: ownership, AttachmentPolicy: repo.AttachmentPolicy,
			State: repo.State, DisplayState: string(repo.DisplayState()), Connectivity: repo.Connectivity,
			LocalRevision: repo.LocalRev, HeadRevision: repo.HeadRev,
			WorkingCopyBytes: repo.WorkingCopyBytes, WorkingCopySizeKnown: repo.WorkingCopySizeKnown,
			PendingFiles: repo.Pending.Added + repo.Pending.Modified + repo.Pending.Deleted,
			PendingBytes: repo.Pending.TotalBytes, Conflicts: repo.Conflicts,
			CurrentOperation: operation, ReservationCount: repo.ReservationCount,
			CanOpen: canOpen, CanLock: canLock, CanUnlock: canUnlock, CanPublish: canPublish, CanReviewQuarantine: canReviewQuarantine,
			Cycle:         CycleProjection{ID: repo.Cycle.ID, Phase: repo.Cycle.Phase, LastTickAt: repo.Cycle.LastTickAt, NextTickAt: repo.Cycle.NextTickAt},
			ServerDeleted: repo.ServerDeleted, LocalCleanupPending: repo.LocalCleanupPending,
			RetainUntil: repo.RetainUntil, RecoveryOperationID: repo.RecoveryOperationID,
			RecoveryAvailable: repo.RecoveryAvailable, RecoveryPending: repo.RecoveryPending, CleanupError: repo.CleanupError,
			Purpose: repo.Purpose,
		})
		if repo.Cycle.Phase == contract.CycleRunning {
			result.CycleRunning = true
		}
		if next, err := time.Parse(time.RFC3339Nano, repo.Cycle.NextTickAt); err == nil {
			current, currentErr := time.Parse(time.RFC3339Nano, result.NextCycleAt)
			if currentErr != nil || next.Before(current) {
				result.NextCycleAt = next.Format(time.RFC3339Nano)
			}
		}
	}
	for _, action := range vm.PendingActions {
		result.PendingActions = append(result.PendingActions, PendingActionProjection{
			ID: action.ID, Kind: action.Kind, RepoID: action.RepoID, ServerID: action.ServerID,
			Label: action.Label, Phase: action.Phase, StartedAt: action.StartedAt.Format(time.RFC3339Nano),
		})
	}
	for _, reservation := range vm.Reservations {
		repository := reservation.RepoID
		for _, repo := range vm.Repos {
			if repo.ID == reservation.RepoID && repo.ServerID == reservation.ServerID {
				if strings.TrimSpace(repo.DisplayName) != "" {
					repository = repo.DisplayName
				}
				break
			}
		}
		result.Reservations = append(result.Reservations, ReservationProjection{
			ID: reservation.ID, ServerID: reservation.ServerID, RepoID: reservation.RepoID,
			Repository: repository, Path: reservation.Path, OwnerLabel: reservation.OwnerLabel,
			CreatedAt:    reservation.CreatedAt,
			CanRelease:   vm.CanReleaseReservations() && reservation.CanRelease,
			LocalChanges: reservation.LocalChanges, ActivePassport: reservation.ActivePassport,
		})
	}
	for _, item := range vm.Errors {
		result.Errors = append(result.Errors, ErrorProjection{
			ID: item.ID, RepoID: item.RepoID, Timestamp: item.Timestamp,
			Code: item.Code, Severity: item.Severity, Hint: item.Hint, Message: item.Message,
		})
	}
	for _, item := range vm.Activity {
		result.Activity = append(result.Activity, ActivityProjection{
			RepoID: item.RepoID, Path: item.Path, Kind: item.Kind, Stage: item.Stage,
			Updated: item.UpdatedAt, Revision: item.Revision, ErrorID: item.ErrorID, Size: item.Size,
		})
	}
	for _, entry := range journal.BuildAt(vm, now) {
		result.Journal = append(result.Journal, JournalProjection{
			ID: entry.ID, RelativeTime: entry.RelativeTime, ExactTime: entry.ExactTime,
			Repository: entry.Repo, Summary: entry.Summary, Details: entry.Details,
			Severity: entry.Severity, Emphasized: entry.Emphasized,
		})
	}
	for _, item := range vm.Notices {
		result.Notices = append(result.Notices, NoticeProjection{
			ID: item.ID, RepoID: item.RepoID, Revision: item.Revision,
			Title: item.Title, CreatedAt: item.CreatedAt, Acked: item.Acked,
			CanAck: !item.Acked && vm.Connected && !vm.Stale && vm.CanAckNotices(),
		})
	}
	result.PublicSharesKnown = vm.PublicSharesKnown
	for _, item := range vm.PublicShares {
		address := strings.Trim(strings.TrimSpace(item.Alias)+"/"+strings.Trim(strings.TrimSpace(item.Slug), "/"), "/")
		canOpen := vm.CanManagePublicShares() && projectedRepoBelongsToServer(vm, item.RepoID, item.ServerID)
		result.PublicShares = append(result.PublicShares, DashboardPublicShareProjection{
			ChannelID: item.ChannelID, ServerID: item.ServerID, RepoID: item.RepoID,
			Repository: firstNonBlank(item.RepoDisplayName, item.RepoID), Address: address,
			State: item.State, SourceRoot: item.SourceRoot, UpdatedAt: item.UpdatedAt,
			RecipientCount: item.RecipientCount, ObjectCount: item.ObjectCount,
			PasswordProtected: item.PasswordProtected, FollowHead: item.FollowHead,
			CanOpen: canOpen, CanRevoke: canOpen && strings.EqualFold(strings.TrimSpace(item.State), "active"),
		})
	}
	sort.SliceStable(result.PublicShares, func(i, j int) bool {
		left, right := result.PublicShares[i], result.PublicShares[j]
		if left.ServerID != right.ServerID {
			return serverProjectionOrder(result.Servers, left.ServerID) < serverProjectionOrder(result.Servers, right.ServerID)
		}
		leftActive, rightActive := strings.EqualFold(left.State, "active"), strings.EqualFold(right.State, "active")
		if leftActive != rightActive {
			return leftActive
		}
		if left.UpdatedAt != right.UpdatedAt {
			return left.UpdatedAt > right.UpdatedAt
		}
		return left.ChannelID < right.ChannelID
	})
	if vm.Update != nil {
		result.Update = &UpdateProjection{
			State: vm.Update.State, CurrentVersion: vm.Update.CurrentVersion,
			AvailableVersion: vm.Update.AvailableVersion, Summary: vm.Update.Summary,
			RestartRequired: vm.Update.RestartRequired,
		}
	}
	return result
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func projectedRepoBelongsToServer(vm guiapp.ViewModel, repoID, serverID string) bool {
	for _, repo := range vm.Repos {
		if repo.ID == repoID && repo.ServerID == serverID && !repo.ServerDeleted {
			return true
		}
	}
	return false
}

func serverProjectionOrder(servers []ServerProjection, serverID string) int {
	for index, server := range servers {
		if server.ID == serverID {
			return index
		}
	}
	return len(servers)
}
