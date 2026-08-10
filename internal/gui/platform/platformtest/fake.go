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
	ConsentFunc         func(context.Context, platform.ConsentRequest) (platform.ConsentResult, error)
	ReservationsFunc    func(context.Context, platform.ReservationDialogRequest) (platform.ReservationDialogResult, error)
	SettingsFunc        func(context.Context, platform.SettingsDialogRequest) (platform.SettingsDialogResult, error)
	JournalFunc         func(context.Context, platform.JournalDialogRequest) error
	RealmGrantsFunc     func(context.Context, platform.RealmGrantDialogRequest) (platform.RealmGrantDialogResult, error)
	RealmVisibilityFunc func(context.Context, platform.RealmVisibilityDialogRequest) (platform.RealmVisibilityDialogResult, error)
	NotifyFunc          func(context.Context, platform.Notification) error
	AutostartStatusFunc func(context.Context, platform.AutostartSpec) (platform.AutostartState, error)
	SetAutostartFunc    func(context.Context, platform.AutostartSpec, bool) error

	mu                      sync.Mutex
	OpenedFolders           []string
	PickRequests            []platform.PickFilesRequest
	FolderRequests          []platform.PickFolderRequest
	PromptRequests          []platform.PromptTextRequest
	InfoRequests            []platform.InfoRequest
	ConfirmRequests         []platform.ConfirmRequest
	ConsentRequests         []platform.ConsentRequest
	ReservationRequests     []platform.ReservationDialogRequest
	SettingsRequests        []platform.SettingsDialogRequest
	JournalRequests         []platform.JournalDialogRequest
	RealmGrantRequests      []platform.RealmGrantDialogRequest
	RealmVisibilityRequests []platform.RealmVisibilityDialogRequest
	Notifications           []platform.Notification
	StatusRequests          []platform.AutostartSpec
	AutostartSets           []AutostartSet
}

func (f *Fake) ShowJournal(ctx context.Context, request platform.JournalDialogRequest) error {
	f.mu.Lock()
	f.JournalRequests = append(f.JournalRequests, request)
	fn := f.JournalFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return nil
}

func (f *Fake) ShowReservations(ctx context.Context, request platform.ReservationDialogRequest) (platform.ReservationDialogResult, error) {
	f.mu.Lock()
	f.ReservationRequests = append(f.ReservationRequests, request)
	fn := f.ReservationsFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.ReservationDialogResult{Action: platform.ReservationDialogClose}, nil
}

func (f *Fake) ShowSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	f.mu.Lock()
	f.SettingsRequests = append(f.SettingsRequests, request)
	fn := f.SettingsFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
}

func (f *Fake) ShowRealmGrants(ctx context.Context, request platform.RealmGrantDialogRequest) (platform.RealmGrantDialogResult, error) {
	f.mu.Lock()
	f.RealmGrantRequests = append(f.RealmGrantRequests, request)
	fn := f.RealmGrantsFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.RealmGrantDialogResult{Action: platform.RealmGrantDialogClose}, nil
}

func (f *Fake) ShowRealmVisibility(ctx context.Context, request platform.RealmVisibilityDialogRequest) (platform.RealmVisibilityDialogResult, error) {
	f.mu.Lock()
	f.RealmVisibilityRequests = append(f.RealmVisibilityRequests, request)
	fn := f.RealmVisibilityFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.RealmVisibilityDialogResult{Action: platform.RealmVisibilityDialogClose}, nil
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

func (f *Fake) ConfirmConsent(ctx context.Context, request platform.ConsentRequest) (platform.ConsentResult, error) {
	f.mu.Lock()
	f.ConsentRequests = append(f.ConsentRequests, request)
	fn := f.ConsentFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, request)
	}
	return platform.ConsentResult{Cancelled: true}, nil
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
		OpenedFolders:           append([]string(nil), f.OpenedFolders...),
		PickRequests:            append([]platform.PickFilesRequest(nil), f.PickRequests...),
		FolderRequests:          append([]platform.PickFolderRequest(nil), f.FolderRequests...),
		PromptRequests:          append([]platform.PromptTextRequest(nil), f.PromptRequests...),
		InfoRequests:            append([]platform.InfoRequest(nil), f.InfoRequests...),
		ConfirmRequests:         append([]platform.ConfirmRequest(nil), f.ConfirmRequests...),
		ConsentRequests:         append([]platform.ConsentRequest(nil), f.ConsentRequests...),
		ReservationRequests:     append([]platform.ReservationDialogRequest(nil), f.ReservationRequests...),
		SettingsRequests:        append([]platform.SettingsDialogRequest(nil), f.SettingsRequests...),
		JournalRequests:         append([]platform.JournalDialogRequest(nil), f.JournalRequests...),
		RealmGrantRequests:      append([]platform.RealmGrantDialogRequest(nil), f.RealmGrantRequests...),
		RealmVisibilityRequests: append([]platform.RealmVisibilityDialogRequest(nil), f.RealmVisibilityRequests...),
		Notifications:           append([]platform.Notification(nil), f.Notifications...),
		StatusRequests:          append([]platform.AutostartSpec(nil), f.StatusRequests...),
		AutostartSets:           append([]AutostartSet(nil), f.AutostartSets...),
	}
}

type Snapshot struct {
	OpenedFolders           []string
	PickRequests            []platform.PickFilesRequest
	FolderRequests          []platform.PickFolderRequest
	PromptRequests          []platform.PromptTextRequest
	InfoRequests            []platform.InfoRequest
	ConfirmRequests         []platform.ConfirmRequest
	ConsentRequests         []platform.ConsentRequest
	ReservationRequests     []platform.ReservationDialogRequest
	SettingsRequests        []platform.SettingsDialogRequest
	JournalRequests         []platform.JournalDialogRequest
	RealmGrantRequests      []platform.RealmGrantDialogRequest
	RealmVisibilityRequests []platform.RealmVisibilityDialogRequest
	Notifications           []platform.Notification
	StatusRequests          []platform.AutostartSpec
	AutostartSets           []AutostartSet
}

var _ platform.Backend = (*Fake)(nil)
