// Package platformtest provides deterministic platform fakes for GUI tests.
package platformtest

import (
	"context"
	"sync"

	"filees/internal/gui/platform"
)

type Fake struct {
	OpenFolderFunc      func(context.Context, string) error
	PickFilesFunc       func(context.Context, platform.PickFilesRequest) (platform.PickFilesResult, error)
	NotifyFunc          func(context.Context, platform.Notification) error
	AutostartStatusFunc func(context.Context, platform.AutostartSpec) (platform.AutostartState, error)
	SetAutostartFunc    func(context.Context, platform.AutostartSpec, bool) error

	mu             sync.Mutex
	OpenedFolders  []string
	PickRequests   []platform.PickFilesRequest
	Notifications  []platform.Notification
	StatusRequests []platform.AutostartSpec
	AutostartSets  []AutostartSet
}

type AutostartSet struct {
	Spec    platform.AutostartSpec
	Enabled bool
}

func (f *Fake) OpenFolder(ctx context.Context, path string) error {
	f.mu.Lock()
	f.OpenedFolders = append(f.OpenedFolders, path)
	fn := f.OpenFolderFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, path)
	}
	return nil
}

func (f *Fake) PickFiles(ctx context.Context, request platform.PickFilesRequest) (platform.PickFilesResult, error) {
	f.mu.Lock()
	f.PickRequests = append(f.PickRequests, request)
	fn := f.PickFilesFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.PickFilesResult{Cancelled: true}, nil
}

func (f *Fake) Notify(ctx context.Context, notification platform.Notification) error {
	f.mu.Lock()
	f.Notifications = append(f.Notifications, notification)
	fn := f.NotifyFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, notification)
	}
	return nil
}

func (f *Fake) AutostartStatus(ctx context.Context, spec platform.AutostartSpec) (platform.AutostartState, error) {
	f.mu.Lock()
	f.StatusRequests = append(f.StatusRequests, spec)
	fn := f.AutostartStatusFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, spec)
	}
	return platform.AutostartState{}, nil
}

func (f *Fake) SetAutostart(ctx context.Context, spec platform.AutostartSpec, enabled bool) error {
	f.mu.Lock()
	f.AutostartSets = append(f.AutostartSets, AutostartSet{Spec: spec, Enabled: enabled})
	fn := f.SetAutostartFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, spec, enabled)
	}
	return nil
}

func (f *Fake) Snapshot() Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Snapshot{
		OpenedFolders:  append([]string(nil), f.OpenedFolders...),
		PickRequests:   append([]platform.PickFilesRequest(nil), f.PickRequests...),
		Notifications:  append([]platform.Notification(nil), f.Notifications...),
		StatusRequests: append([]platform.AutostartSpec(nil), f.StatusRequests...),
		AutostartSets:  append([]AutostartSet(nil), f.AutostartSets...),
	}
}

type Snapshot struct {
	OpenedFolders  []string
	PickRequests   []platform.PickFilesRequest
	Notifications  []platform.Notification
	StatusRequests []platform.AutostartSpec
	AutostartSets  []AutostartSet
}

var _ platform.Backend = (*Fake)(nil)
