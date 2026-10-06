package main

import (
	"context"
	contract "filees/pkg/contract/v1"
	"time"
)

type virtualFolderDaemon interface {
	VirtualFolders(context.Context, *bool) (*contract.VirtualFoldersStatus, error)
}

// The checkbox is local to this installation, not a server or renderer setting.
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
	service.mu.Unlock()
	defer func() { service.mu.Lock(); service.virtualBusy = false; service.mu.Unlock() }()
	vm := service.gui.viewModel()
	if !vm.Connected || vm.Stale || !vm.HasCap(contract.CapVirtualFolders) || vm.VirtualFolders == nil {
		return SettingsAcceptance{Code: "virtualFolders.unavailable"}
	}
	finish, err := service.gui.beginDisplayCall()
	if err != nil {
		return SettingsAcceptance{Code: "virtualFolders.unavailable"}
	}
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := service.virtualClient.VirtualFolders(ctx, &enabled)
	// No automatic retry: a lost response may follow a durable change.
	if err != nil || result == nil || result.Enabled != enabled {
		return SettingsAcceptance{Code: "virtualFolders.notConfirmed"}
	}
	service.mu.Lock()
	if service.active == session && service.snapshot.Revision == revision {
		state := *result
		service.snapshot.VirtualFolders = &state
	}
	projection, emitter := service.snapshot, service.emitter
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(settingsSnapshotEvent, projection)
	}
	if service.gui.runner != nil {
		service.gui.Refresh()
	}
	return SettingsAcceptance{Accepted: true}
}
