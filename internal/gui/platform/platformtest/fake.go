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
	PickFolderFunc      func(context.Context, platform.PickFolderRequest) (platform.PickFolderResult, error)
	PromptTextFunc      func(context.Context, platform.PromptTextRequest) (platform.PromptTextResult, error)
	ShowInfoFunc        func(context.Context, platform.InfoRequest) error
	ConfirmFunc         func(context.Context, platform.ConfirmRequest) (bool, error)
	NotifyFunc          func(context.Context, platform.Notification) error
	AutostartStatusFunc func(context.Context, platform.AutostartSpec) (platform.AutostartState, error)
	SetAutostartFunc    func(context.Context, platform.AutostartSpec, bool) error

	mu              sync.Mutex
	OpenedFolders   []string
	PickRequests    []platform.PickFilesRequest
	FolderRequests  []platform.PickFolderRequest
	PromptRequests  []platform.PromptTextRequest
	InfoRequests    []platform.InfoRequest
	ConfirmRequests []platform.ConfirmRequest
	Notifications   []platform.Notification
	StatusRequests  []platform.AutostartSpec
	AutostartSets   []AutostartSet
}

func (f *Fake) PickFolder(ctx context.Context, request platform.PickFolderRequest) (platform.PickFolderResult, error) {
	f.mu.Lock()
	f.FolderRequests = append(f.FolderRequests, request)
	fn := f.PickFolderFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.PickFolderResult{Cancelled: true}, nil
}

func (f *Fake) Confirm(ctx context.Context, request platform.ConfirmRequest) (bool, error) {
	f.mu.Lock()
	f.ConfirmRequests = append(f.ConfirmRequests, request)
	fn := f.ConfirmFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return false, nil
}

func (f *Fake) ShowInfo(ctx context.Context, request platform.InfoRequest) error {
	f.mu.Lock()
	f.InfoRequests = append(f.InfoRequests, request)
	fn := f.ShowInfoFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return nil
}

func (f *Fake) PromptText(ctx context.Context, request platform.PromptTextRequest) (platform.PromptTextResult, error) {
	f.mu.Lock()
	f.PromptRequests = append(f.PromptRequests, request)
	fn := f.PromptTextFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.PromptTextResult{Cancelled: true}, nil
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
		OpenedFolders:   append([]string(nil), f.OpenedFolders...),
		PickRequests:    append([]platform.PickFilesRequest(nil), f.PickRequests...),
		FolderRequests:  append([]platform.PickFolderRequest(nil), f.FolderRequests...),
		PromptRequests:  append([]platform.PromptTextRequest(nil), f.PromptRequests...),
		InfoRequests:    append([]platform.InfoRequest(nil), f.InfoRequests...),
		ConfirmRequests: append([]platform.ConfirmRequest(nil), f.ConfirmRequests...),
		Notifications:   append([]platform.Notification(nil), f.Notifications...),
		StatusRequests:  append([]platform.AutostartSpec(nil), f.StatusRequests...),
		AutostartSets:   append([]AutostartSet(nil), f.AutostartSets...),
	}
}

type Snapshot struct {
	OpenedFolders   []string
	PickRequests    []platform.PickFilesRequest
	FolderRequests  []platform.PickFolderRequest
	PromptRequests  []platform.PromptTextRequest
	InfoRequests    []platform.InfoRequest
	ConfirmRequests []platform.ConfirmRequest
	Notifications   []platform.Notification
	StatusRequests  []platform.AutostartSpec
	AutostartSets   []AutostartSet
}

var _ platform.Backend = (*Fake)(nil)
