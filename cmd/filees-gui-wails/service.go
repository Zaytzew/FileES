package main

import (
	"context"
	"sort"
	"sync"
	"time"

	guiapp "filees/internal/gui/app"
)

const snapshotEvent = "filees:snapshot"

type snapshotEmitter interface {
	Emit(name string, data ...any) bool
}

// GUIService is the entire Wails-to-FileES bridge.  It owns no domain state:
// internal/gui/app reconstructs the authoritative presentation from IPC and
// this service only publishes an immutable browser-friendly projection.
type GUIService struct {
	mu       sync.RWMutex
	snapshot Snapshot
	runner   *guiapp.App
	emitter  snapshotEmitter
}

type Snapshot struct {
	Revision     uint64               `json:"revision"`
	Connected    bool                 `json:"connected"`
	Stale        bool                 `json:"stale"`
	DaemonState  string               `json:"daemon_state"`
	UptimeSec    int64                `json:"uptime_sec"`
	LastRefresh  string               `json:"last_refresh,omitempty"`
	IconState    string               `json:"icon_state"`
	Capabilities []string             `json:"capabilities"`
	Servers      []ServerProjection   `json:"servers"`
	Repositories []RepoProjection     `json:"repositories"`
	Errors       []ErrorProjection    `json:"errors"`
	Activity     []ActivityProjection `json:"activity"`
	Notices      []NoticeProjection   `json:"notices"`
	Update       *UpdateProjection    `json:"update,omitempty"`
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
	ID               string `json:"id"`
	ServerID         string `json:"server_id"`
	DisplayName      string `json:"display_name"`
	LocalPath        string `json:"local_path,omitempty"`
	URL              string `json:"url,omitempty"`
	Attached         bool   `json:"attached"`
	Access           string `json:"access"`
	AttachmentPolicy string `json:"attachment_policy"`
	State            string `json:"state"`
	DisplayState     string `json:"display_state"`
	Connectivity     string `json:"connectivity"`
	LocalRevision    int64  `json:"local_revision"`
	HeadRevision     int64  `json:"head_revision"`
	PendingFiles     int    `json:"pending_files"`
	PendingBytes     int64  `json:"pending_bytes"`
	Conflicts        int    `json:"conflicts"`
	CurrentOperation string `json:"current_operation,omitempty"`
	ReservationCount int    `json:"reservation_count"`
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
}

type NoticeProjection struct {
	ID        string `json:"id"`
	RepoID    string `json:"repo_id,omitempty"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
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
			Stale:        true,
			IconState:    string(guiapp.IconDisconnected),
			Capabilities: []string{},
			Servers:      []ServerProjection{},
			Repositories: []RepoProjection{},
			Errors:       []ErrorProjection{},
			Activity:     []ActivityProjection{},
			Notices:      []NoticeProjection{},
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
	emitter := service.emitter
	service.mu.Unlock()

	if emitter != nil {
		emitter.Emit(snapshotEvent, next)
	}
}

func projectViewModel(vm guiapp.ViewModel) Snapshot {
	result := Snapshot{
		Connected:    vm.Connected,
		Stale:        vm.Stale,
		DaemonState:  vm.DaemonState,
		UptimeSec:    vm.UptimeSec,
		IconState:    string(vm.Icon),
		Capabilities: make([]string, 0, len(vm.Capabilities)),
		Servers:      make([]ServerProjection, 0, len(vm.Servers)),
		Repositories: make([]RepoProjection, 0, len(vm.Repos)),
		Errors:       make([]ErrorProjection, 0, len(vm.Errors)),
		Activity:     make([]ActivityProjection, 0, len(vm.Activity)),
		Notices:      make([]NoticeProjection, 0, len(vm.Notices)),
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
	for _, repo := range vm.Repos {
		operation := ""
		if repo.CurrentOp != nil {
			operation = *repo.CurrentOp
		}
		result.Repositories = append(result.Repositories, RepoProjection{
			ID: repo.ID, ServerID: repo.ServerID, DisplayName: repo.DisplayName,
			LocalPath: repo.LocalPath, URL: repo.URL, Attached: repo.Attached,
			Access: repo.Access, AttachmentPolicy: repo.AttachmentPolicy,
			State: repo.State, DisplayState: string(repo.DisplayState()), Connectivity: repo.Connectivity,
			LocalRevision: repo.LocalRev, HeadRevision: repo.HeadRev,
			PendingFiles: repo.Pending.Added + repo.Pending.Modified + repo.Pending.Deleted,
			PendingBytes: repo.Pending.TotalBytes, Conflicts: repo.Conflicts,
			CurrentOperation: operation, ReservationCount: repo.ReservationCount,
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
			Updated: item.UpdatedAt, Revision: item.Revision, ErrorID: item.ErrorID,
		})
	}
	for _, item := range vm.Notices {
		result.Notices = append(result.Notices, NoticeProjection{
			ID: item.ID, RepoID: item.RepoID, Title: item.Title, CreatedAt: item.CreatedAt,
		})
	}
	if vm.Update != nil {
		result.Update = &UpdateProjection{
			State: vm.Update.State, CurrentVersion: vm.Update.CurrentVersion,
			AvailableVersion: vm.Update.AvailableVersion, Summary: vm.Update.Summary,
			RestartRequired: vm.Update.RestartRequired,
		}
	}
	return result
}
