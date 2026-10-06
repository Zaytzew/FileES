package main

import (
	"context"
	guiapp "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
	"time"
)

type virtualFolderDaemon interface {
	VirtualFolders(context.Context, string, *bool) (*contract.VirtualFoldersStatus, error)
}

// The checkbox is local to this installation and selected server.
// The daemon serialises lifecycle decisions and owns durable storage. Never
// infer enablement from the GUI's build, environment or an existing point.
func (service *SettingsService) VirtualFolders(enabled bool, revision uint64) SettingsAcceptance {
	service.mu.Lock()
	if service.active == nil || service.snapshot.Revision != revision || service.snapshot.VirtualFolders == nil || service.virtualBusy || service.virtualClient == nil || service.gui == nil {
		service.mu.Unlock()
		return SettingsAcceptance{Code: "virtualFolders.unavailable"}
	}
	service.virtualBusy = true
	session := service.active
	serverID := service.snapshot.Server.ID
	service.mu.Unlock()
	defer func() { service.mu.Lock(); service.virtualBusy = false; service.mu.Unlock() }()
	return service.applyVirtualFolders(serverID, enabled, revision, session)
}

// DashboardVirtualFolders shares the checkbox's daemon call and mutation
// fence. Its revision is the main panel's, not an inactive settings session's.
func (service *SettingsService) DashboardVirtualFolders(serverID string, enabled bool, revision uint64) SettingsAcceptance {
	service.mu.Lock()
	if service.active != nil || service.virtualBusy || service.virtualClient == nil || service.gui == nil {
		service.mu.Unlock()
		return SettingsAcceptance{Code: "virtualFolders.unavailable"}
	}
	service.virtualBusy = true
	service.mu.Unlock()
	defer func() { service.mu.Lock(); service.virtualBusy = false; service.mu.Unlock() }()
	current := service.gui.Snapshot()
	var state *contract.VirtualFoldersStatus
	for _, server := range current.Servers {
		if server.ID == serverID {
			state = server.VirtualFolders
			break
		}
	}
	if current.Revision != revision || state == nil || state.Enabled == enabled {
		return SettingsAcceptance{Code: "virtualFolders.unavailable"}
	}
	return service.applyVirtualFolders(serverID, enabled, revision, nil)
}

// Server presence and the new daemon capability are required, never guessed
// from a global preference or the GUI build. Returned state is detached.
func virtualFoldersForServer(vm guiapp.ViewModel, serverID string) *contract.VirtualFoldersStatus {
	if serverID == "" || !vm.Connected || vm.Stale || !vm.HasCap(contract.CapVirtualFolders) || vm.VirtualFolders == nil {
		return nil
	}
	for _, server := range vm.Servers {
		if server.ID == serverID {
			return &contract.VirtualFoldersStatus{ServerID: serverID, Enabled: vm.VirtualFolders.Servers[serverID]}
		}
	}
	return nil
}

func (service *SettingsService) applyVirtualFolders(serverID string, enabled bool, revision uint64, session *settingsSession) SettingsAcceptance {
	vm := service.gui.viewModel()
	if virtualFoldersForServer(vm, serverID) == nil {
		return SettingsAcceptance{Code: "virtualFolders.unavailable"}
	}
	finish, err := service.gui.beginDisplayCall()
	if err != nil {
		return SettingsAcceptance{Code: "virtualFolders.unavailable"}
	}
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := service.virtualClient.VirtualFolders(ctx, serverID, &enabled)
	// No automatic retry: a lost response may follow a durable change.
	if err != nil || result == nil || result.ServerID != serverID || result.Enabled != enabled {
		return SettingsAcceptance{Code: "virtualFolders.notConfirmed"}
	}
	service.mu.Lock()
	if session != nil && service.active == session && service.snapshot.Revision == revision {
		state := *result
		service.snapshot.VirtualFolders = &state
	}
	projection, emitter := service.snapshot, service.emitter
	service.mu.Unlock()
	if session != nil && emitter != nil {
		emitter.Emit(settingsSnapshotEvent, projection)
	}
	if service.gui.runner != nil {
		service.gui.Refresh()
	}
	return SettingsAcceptance{Accepted: true}
}
