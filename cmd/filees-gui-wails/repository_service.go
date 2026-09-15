package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"filees/internal/gui/platform"
)

const repositorySnapshotEvent = "filees:repository-snapshot"

// RepositoryService owns the presentation session for one repository window.
// It receives already-authorised models from the shared controller and returns
// only opaque IDs plus closed action enums. It never calls daemon IPC.
type RepositoryService struct {
	mu         sync.RWMutex
	snapshot   RepositorySnapshot
	emitter    snapshotEmitter
	show       func()
	hide       func()
	settings   *repositorySettingsSession
	shares     *repositorySharesSession
	grants     *repositoryGrantsSession
	uploads    *repositoryUploadsSession
	quarantine *repositoryQuarantineSession
	shelf      *repositoryShelfSession
	// pendingShares binds a controller continuation to the exact repository
	// action that requested it. A newer gear click clears the continuation.
	pendingShares     string
	pendingGrants     string
	pendingUploads    string
	pendingQuarantine string
	pendingShelf      string
	revision          uint64
}

type repositorySettingsSession struct {
	result   chan platform.SettingsDialogResult
	resolved bool
}

type repositorySharesSession struct {
	result   chan platform.PublicShareDialogResult
	resolved bool
}

type repositoryGrantsSession struct {
	result   chan platform.RealmGrantDialogResult
	resolved bool
}

type repositoryUploadsSession struct {
	result   chan platform.UploadChannelDialogResult
	resolved bool
}

type repositoryQuarantineSession struct {
	result   chan platform.QuarantineDialogResult
	resolved bool
}

type repositoryShelfSession struct {
	result   chan platform.ShelfDialogResult
	resolved bool
}

type repositorySettingsBrowserAdapter struct{ service *RepositoryService }
type repositoryPublicShareBrowserAdapter struct{ service *RepositoryService }
type repositoryRealmGrantBrowserAdapter struct {
	service  *RepositoryService
	prompter pairingServerSelector
}
type repositoryUploadChannelBrowserAdapter struct{ service *RepositoryService }
type repositoryQuarantineBrowserAdapter struct{ service *RepositoryService }
type repositoryShelfBrowserAdapter struct{ service *RepositoryService }

type settingsBrowserRouter struct {
	server     settingsBrowserAdapter
	repository repositorySettingsBrowserAdapter
}

type RepositorySnapshot struct {
	ShelfCanImport bool                         `json:"shelf_can_import"`
	TextKey        string                       `json:"text_key,omitempty"`
	TextPrefix     string                       `json:"text_prefix,omitempty"`
	Revision       uint64                       `json:"revision"`
	Mode           string                       `json:"mode"`
	Title          string                       `json:"title"`
	Text           string                       `json:"text"`
	Busy           bool                         `json:"busy"`
	Context        RepositoryContextProjection  `json:"context"`
	Actions        []RepositoryActionProjection `json:"actions"`
	Shares         []PublicShareProjection      `json:"shares"`
	Grants         []RealmGrantProjection       `json:"grants"`
	Uploads        []UploadChannelProjection    `json:"uploads"`
	Quarantine     []QuarantineItemProjection   `json:"quarantine"`
	Shelf          []ShelfItemProjection        `json:"shelf"`
	ShelfName      string                       `json:"shelf_name,omitempty"`
	FocusChannelID string                       `json:"focus_channel_id,omitempty"`
}

type RepositoryContextProjection struct {
	StateKey        string `json:"state_key,omitempty"`
	AccessKey       string `json:"access_key,omitempty"`
	EditingKey      string `json:"editing_key,omitempty"`
	LastCommitAt    string `json:"last_commit_at,omitempty"`
	CanFoldInactive bool   `json:"can_fold_inactive"`
	ServerID        string `json:"server_id"`
	ServerName      string `json:"server_name"`
	Address         string `json:"address"`
	Realm           string `json:"realm"`
	RepoID          string `json:"repo_id"`
	Name            string `json:"name"`
	LocalPath       string `json:"local_path"`
	State           string `json:"state"`
	Access          string `json:"access"`
	Editing         string `json:"editing"`
}

type RepositoryActionProjection struct {
	ID             string `json:"id"`
	LabelKey       string `json:"label_key"`
	DescriptionKey string `json:"description_key"`
	Tone           string `json:"tone"`
}

type PublicShareProjection struct {
	StateKey   string `json:"state_key,omitempty"`
	ChannelID  string `json:"channel_id"`
	Address    string `json:"address"`
	State      string `json:"state"`
	SourceRoot string `json:"source_root"`
	Recipients string `json:"recipients"`
	Password   string `json:"password"`
	Revision   string `json:"revision"`
	CanEdit    bool   `json:"can_edit"`
	CanRevoke  bool   `json:"can_revoke"`
	CanDelete  bool   `json:"can_delete"`
}

type RealmGrantProjection struct {
	RealmID   string `json:"realm_id"`
	Alias     string `json:"alias"`
	Access    string `json:"access"`
	State     string `json:"state"`
	CanRead   bool   `json:"can_read"`
	CanWrite  bool   `json:"can_write"`
	CanRevoke bool   `json:"can_revoke"`
}

type UploadChannelProjection struct {
	StateKey   string `json:"state_key,omitempty"`
	ChannelID  string `json:"channel_id"`
	Address    string `json:"address"`
	State      string `json:"state"`
	Recipients string `json:"recipients"`
	RequireOTP bool   `json:"require_otp,omitempty"`
	CanEdit    bool   `json:"can_edit"`
	CanRevoke  bool   `json:"can_revoke"`
	CanDelete  bool   `json:"can_delete"`
}

// ShelfItemProjection is one delivered file as the panel renders it. There is
// no size label shortcut here as quarantine has: a shelf shows the repository
// path too, so the renderer formats both and the host keeps the raw numbers.
type ShelfItemProjection struct {
	UploadID     string `json:"upload_id"`
	RepoPath     string `json:"repo_path"`
	OriginalName string `json:"original_name"`
	Size         int64  `json:"size"`
	SizeLabel    string `json:"size_label"`
	Revision     int64  `json:"revision,omitempty"`
	AcceptedAt   string `json:"accepted_at,omitempty"`
}

type QuarantineItemProjection struct {
	UploadID       string `json:"upload_id"`
	OriginalName   string `json:"original_name"`
	Size           int64  `json:"size"`
	SizeLabel      string `json:"size_label"`
	AVVerdict      string `json:"av_verdict,omitempty"`
	RemainingHours int    `json:"remaining_hours"`
}

type RepositoryChoice struct {
	Action    string `json:"action"`
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	ChannelID string `json:"channel_id,omitempty"`
	RealmID   string `json:"realm_id,omitempty"`
	UploadID  string `json:"upload_id,omitempty"`
}

type RepositoryAcceptance struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
}

func newRepositoryService() *RepositoryService {
	return &RepositoryService{snapshot: emptyRepositorySnapshot()}
}

func emptyRepositorySnapshot() RepositorySnapshot {
	return RepositorySnapshot{Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{}, Uploads: []UploadChannelProjection{}, Quarantine: []QuarantineItemProjection{}}
}

func (service *RepositoryService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

func (service *RepositoryService) attachPresentation(show, hide func()) {
	service.mu.Lock()
	service.show = show
	service.hide = hide
	service.mu.Unlock()
}

func (service *RepositoryService) Snapshot() RepositorySnapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.snapshot
}

// ChooseAction returns one action from the current repository projection.
func (service *RepositoryService) ChooseAction(choice RepositoryChoice) RepositoryAcceptance {
	choice = trimRepositoryChoice(choice)
	service.mu.Lock()
	session := service.settings
	snapshot := service.snapshot
	if session == nil {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_settings_inactive"}
	}
	if !sameRepositoryContext(snapshot, choice) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_context_changed"}
	}
	if session.resolved {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_choice_busy"}
	}
	action, ok := projectedRepositoryAction(snapshot.Actions, choice.Action)
	if !ok {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_action_unavailable"}
	}
	session.resolved = true
	if action.ID == string(platform.SettingsDialogPublicShares) {
		service.pendingShares = repositoryContextKey(choice.ServerID, choice.RepoID)
	} else if action.ID == string(platform.SettingsDialogManageGrants) {
		service.pendingGrants = repositoryContextKey(choice.ServerID, choice.RepoID)
	} else if action.ID == string(platform.SettingsDialogUploadChannels) {
		service.pendingUploads = repositoryContextKey(choice.ServerID, choice.RepoID)
	} else if action.ID == string(platform.SettingsDialogQuarantine) {
		service.pendingQuarantine = repositoryContextKey(choice.ServerID, choice.RepoID)
	}
	hide := service.hide
	service.mu.Unlock()

	result := platform.SettingsDialogResult{Action: platform.SettingsDialogAction(action.ID), ServerID: choice.ServerID, RepoID: choice.RepoID}
	if result.Action == platform.SettingsDialogConnectRepos {
		result.RepoIDs = []string{choice.RepoID}
	}
	session.result <- result
	if hide != nil {
		hide()
	}
	return RepositoryAcceptance{Accepted: true}
}

// ChooseUpload returns an action only for a shelf present in the current
// authoritative channel list. Create is the sole channel-less operation.
func (service *RepositoryService) ChooseUpload(choice RepositoryChoice) RepositoryAcceptance {
	choice = trimRepositoryChoice(choice)
	service.mu.Lock()
	session := service.uploads
	snapshot := service.snapshot
	if session == nil {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "upload_channels_inactive"}
	}
	if !sameRepositoryContext(snapshot, choice) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_context_changed"}
	}
	if session.resolved {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_choice_busy"}
	}
	action := platform.UploadChannelDialogAction(choice.Action)
	if !uploadChoiceAllowed(snapshot.Uploads, action, choice.ChannelID) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "upload_channel_action_unavailable"}
	}
	session.resolved = true
	service.pendingUploads = repositoryContextKey(choice.ServerID, choice.RepoID)
	hide := service.hide
	service.mu.Unlock()

	session.result <- platform.UploadChannelDialogResult{Action: action, ChannelID: choice.ChannelID}
	if hide != nil {
		hide()
	}
	return RepositoryAcceptance{Accepted: true}
}

// ChooseQuarantine returns fetch or hide only for an item in the current
// waiting-room projection. Closing is Cancel, not this method.
func (service *RepositoryService) ChooseQuarantine(choice RepositoryChoice) RepositoryAcceptance {
	choice = trimRepositoryChoice(choice)
	service.mu.Lock()
	session := service.quarantine
	snapshot := service.snapshot
	if session == nil {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "quarantine_inactive"}
	}
	if !sameRepositoryContext(snapshot, choice) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_context_changed"}
	}
	if session.resolved {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_choice_busy"}
	}
	action := platform.QuarantineDialogAction(choice.Action)
	if !quarantineChoiceAllowed(snapshot.Quarantine, action, choice.UploadID) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "quarantine_action_unavailable"}
	}
	session.resolved = true
	service.pendingQuarantine = repositoryContextKey(choice.ServerID, choice.RepoID)
	hide := service.hide
	service.mu.Unlock()

	session.result <- platform.QuarantineDialogResult{Action: action, UploadID: choice.UploadID}
	if hide != nil {
		hide()
	}
	return RepositoryAcceptance{Accepted: true}
}

// ChooseShelf accepts only a displayed selection from this presentation session.
func (service *RepositoryService) ChooseShelf(choice RepositoryChoice) RepositoryAcceptance {
	choice = trimRepositoryChoice(choice)
	service.mu.Lock()
	session := service.shelf
	snapshot := service.snapshot
	if session == nil || session.resolved || !sameRepositoryContext(snapshot, choice) || (choice.Action != "fetch" && choice.Action != "import") || (choice.Action == "import" && !snapshot.ShelfCanImport) || choice.ChannelID != snapshot.FocusChannelID {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "shelf_action_unavailable"}
	}
	found := false
	for _, item := range snapshot.Shelf {
		if item.UploadID == choice.UploadID {
			found = true
			break
		}
	}
	if !found {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "shelf_item_unavailable"}
	}
	session.resolved = true
	hide := service.hide
	service.mu.Unlock()
	session.result <- platform.ShelfDialogResult{Action: platform.ShelfDialogAction(choice.Action), UploadID: choice.UploadID}
	if hide != nil {
		hide()
	}
	return RepositoryAcceptance{Accepted: true}
}

// ChooseGrant returns an action only for a recipient projected by the current
// grant directory. Realm identifiers stay opaque and are never accepted from
// an older or foreign repository session.
func (service *RepositoryService) ChooseGrant(choice RepositoryChoice) RepositoryAcceptance {
	choice = trimRepositoryChoice(choice)
	service.mu.Lock()
	session := service.grants
	snapshot := service.snapshot
	if session == nil {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "realm_grants_inactive"}
	}
	if !sameRepositoryContext(snapshot, choice) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_context_changed"}
	}
	if session.resolved {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_choice_busy"}
	}
	action := platform.RealmGrantDialogAction(choice.Action)
	if !grantChoiceAllowed(snapshot.Grants, action, choice.RealmID) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "realm_grant_action_unavailable"}
	}
	session.resolved = true
	service.pendingGrants = repositoryContextKey(choice.ServerID, choice.RepoID)
	hide := service.hide
	service.mu.Unlock()

	session.result <- platform.RealmGrantDialogResult{Action: action, RealmID: choice.RealmID}
	if hide != nil {
		hide()
	}
	return RepositoryAcceptance{Accepted: true}
}

// ChooseShare returns an action only for a channel present in the current
// authoritative list. Create is the sole channel-less operation.
func (service *RepositoryService) ChooseShare(choice RepositoryChoice) RepositoryAcceptance {
	choice = trimRepositoryChoice(choice)
	service.mu.Lock()
	session := service.shares
	snapshot := service.snapshot
	if session == nil {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "public_shares_inactive"}
	}
	if !sameRepositoryContext(snapshot, choice) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_context_changed"}
	}
	if session.resolved {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "repository_choice_busy"}
	}
	action := platform.PublicShareDialogAction(choice.Action)
	if !shareChoiceAllowed(snapshot.Shares, action, choice.ChannelID) {
		service.mu.Unlock()
		return RepositoryAcceptance{Code: "public_share_action_unavailable"}
	}
	session.resolved = true
	service.pendingShares = repositoryContextKey(choice.ServerID, choice.RepoID)
	hide := service.hide
	service.mu.Unlock()

	session.result <- platform.PublicShareDialogResult{Action: action, ChannelID: choice.ChannelID}
	if hide != nil {
		hide()
	}
	return RepositoryAcceptance{Accepted: true}
}

// Cancel closes only the repository presentation session.
func (service *RepositoryService) Cancel() {
	service.mu.Lock()
	settings := service.settings
	shares := service.shares
	grants := service.grants
	uploads := service.uploads
	quarantine := service.quarantine
	shelf := service.shelf
	hide := service.hide
	resolveSettings := settings != nil && !settings.resolved
	resolveShares := shares != nil && !shares.resolved
	resolveGrants := grants != nil && !grants.resolved
	resolveUploads := uploads != nil && !uploads.resolved
	resolveQuarantine := quarantine != nil && !quarantine.resolved
	resolveShelf := shelf != nil && !shelf.resolved
	if resolveSettings {
		settings.resolved = true
	}
	if resolveShares {
		shares.resolved = true
	}
	if resolveGrants {
		grants.resolved = true
	}
	if resolveUploads {
		uploads.resolved = true
	}
	if resolveQuarantine {
		quarantine.resolved = true
	}
	if resolveShelf {
		shelf.resolved = true
	}
	service.pendingShares = ""
	service.pendingGrants = ""
	service.pendingUploads = ""
	service.pendingQuarantine = ""
	service.mu.Unlock()
	if resolveSettings {
		settings.result <- platform.SettingsDialogResult{Action: platform.SettingsDialogClose}
	}
	if resolveShares {
		shares.result <- platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}
	}
	if resolveGrants {
		grants.result <- platform.RealmGrantDialogResult{Action: platform.RealmGrantDialogClose}
	}
	if resolveUploads {
		uploads.result <- platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}
	}
	if resolveQuarantine {
		quarantine.result <- platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}
	}
	if resolveShelf {
		shelf.result <- platform.ShelfDialogResult{Action: platform.ShelfDialogClose}
	}
	if hide != nil {
		hide()
	}
}

func (router settingsBrowserRouter) ShowSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	if strings.TrimSpace(request.FocusRepoID) != "" {
		return router.repository.ShowSettings(ctx, request)
	}
	return router.server.ShowSettings(ctx, request)
}

func (adapter repositorySettingsBrowserAdapter) ShowSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	return adapter.service.showSettings(ctx, request)
}

func (adapter repositoryPublicShareBrowserAdapter) ShowPublicShares(ctx context.Context, request platform.PublicShareDialogRequest) (platform.PublicShareDialogResult, error) {
	return adapter.service.showPublicShares(ctx, request)
}

func (adapter repositoryRealmGrantBrowserAdapter) ShowRealmGrants(ctx context.Context, request platform.RealmGrantDialogRequest) (platform.RealmGrantDialogResult, error) {
	return adapter.service.showRealmGrants(ctx, request)
}

func (adapter repositoryRealmGrantBrowserAdapter) ShowRealmVisibility(ctx context.Context, request platform.RealmVisibilityDialogRequest) (platform.RealmVisibilityDialogResult, error) {
	if adapter.prompter == nil {
		return platform.RealmVisibilityDialogResult{Action: platform.RealmVisibilityDialogClose}, nil
	}
	choice, err := adapter.prompter.SelectOne(ctx, PromptSelectRequest{
		PresentationKey: "select.visibility", PresentationArgs: map[string]string{"name": request.RealmName},
		Title: request.Title, Text: request.Text,
		Options: []PromptOption{{Value: "listed", Label: "Visible"}, {Value: "hidden", Label: "Hidden"}},
	})
	result := platform.RealmVisibilityDialogResult{Action: platform.RealmVisibilityDialogClose}
	if err != nil || choice.Cancelled {
		return result, err
	}
	switch choice.Value {
	case "listed":
		result.Action = platform.RealmVisibilityDialogListed
	case "hidden":
		result.Action = platform.RealmVisibilityDialogPrivate
	}
	return result, nil
}

func (adapter repositoryUploadChannelBrowserAdapter) ShowUploadChannels(ctx context.Context, request platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
	return adapter.service.showUploadChannels(ctx, request)
}

func (adapter repositoryQuarantineBrowserAdapter) ShowQuarantine(ctx context.Context, request platform.QuarantineDialogRequest) (platform.QuarantineDialogResult, error) {
	return adapter.service.showQuarantine(ctx, request)
}

func (service *RepositoryService) showSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	projection, ok := projectRepositorySettings(request)
	if !ok {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
	}
	session := &repositorySettingsSession{result: make(chan platform.SettingsDialogResult, 1)}

	service.mu.Lock()
	previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine := service.settings, service.shares, service.grants, service.uploads, service.quarantine
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = session
	service.shares = nil
	service.grants = nil
	service.uploads = nil
	service.quarantine = nil
	service.pendingShares = ""
	service.pendingGrants = ""
	service.pendingUploads = ""
	service.pendingQuarantine = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine)
	emitRepositorySnapshot(emitter, projection)
	if show != nil {
		show()
	}

	select {
	case result := <-session.result:
		service.finishSettings(session)
		return result, nil
	case <-ctx.Done():
		service.finishSettings(session)
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, ctx.Err()
	}
}

func (service *RepositoryService) showPublicShares(ctx context.Context, request platform.PublicShareDialogRequest) (platform.PublicShareDialogResult, error) {
	projection, ok := projectPublicShares(request)
	if !ok {
		return platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}, nil
	}
	session := &repositorySharesSession{result: make(chan platform.PublicShareDialogResult, 1)}

	service.mu.Lock()
	if request.DirectEntry {
		service.pendingShares = repositoryContextKey(request.ServerID, request.RepoID)
	}
	if service.pendingShares != repositoryContextKey(request.ServerID, request.RepoID) {
		service.mu.Unlock()
		return platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}, nil
	}
	previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine := service.settings, service.shares, service.grants, service.uploads, service.quarantine
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = session
	service.grants = nil
	service.uploads = nil
	service.quarantine = nil
	service.pendingShares = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine)
	emitRepositorySnapshot(emitter, projection)
	if show != nil {
		show()
	}

	select {
	case result := <-session.result:
		service.finishShares(session)
		return result, nil
	case <-ctx.Done():
		service.finishShares(session)
		return platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}, ctx.Err()
	}
}

func (service *RepositoryService) showRealmGrants(ctx context.Context, request platform.RealmGrantDialogRequest) (platform.RealmGrantDialogResult, error) {
	service.mu.Lock()
	contextProjection := service.snapshot.Context
	pending := service.pendingGrants
	if pending == "" || pending != repositoryContextKey(contextProjection.ServerID, contextProjection.RepoID) {
		service.mu.Unlock()
		return platform.RealmGrantDialogResult{Action: platform.RealmGrantDialogClose}, nil
	}
	projection, ok := projectRealmGrants(request, contextProjection)
	if !ok {
		service.pendingGrants = ""
		service.mu.Unlock()
		return platform.RealmGrantDialogResult{Action: platform.RealmGrantDialogClose}, nil
	}
	session := &repositoryGrantsSession{result: make(chan platform.RealmGrantDialogResult, 1)}
	previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine := service.settings, service.shares, service.grants, service.uploads, service.quarantine
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = nil
	service.grants = session
	service.uploads = nil
	service.quarantine = nil
	service.pendingGrants = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine)
	emitRepositorySnapshot(emitter, projection)
	if show != nil {
		show()
	}

	select {
	case result := <-session.result:
		service.finishGrants(session)
		return result, nil
	case <-ctx.Done():
		service.finishGrants(session)
		return platform.RealmGrantDialogResult{Action: platform.RealmGrantDialogClose}, ctx.Err()
	}
}

func (service *RepositoryService) showUploadChannels(ctx context.Context, request platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
	service.mu.Lock()
	if request.DirectEntry && request.ServerID != "" && request.RepoID != "" {
		service.pendingUploads = repositoryContextKey(request.ServerID, request.RepoID)
		service.snapshot.Context = RepositoryContextProjection{ServerID: request.ServerID, RepoID: request.RepoID, Name: request.RepositoryName}
	}
	contextProjection := service.snapshot.Context
	pending := service.pendingUploads
	if pending == "" || pending != repositoryContextKey(contextProjection.ServerID, contextProjection.RepoID) {
		service.mu.Unlock()
		return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
	}
	projection, ok := projectUploadChannels(request, contextProjection)
	if !ok {
		service.pendingUploads = ""
		service.mu.Unlock()
		return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, nil
	}
	session := &repositoryUploadsSession{result: make(chan platform.UploadChannelDialogResult, 1)}
	previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine := service.settings, service.shares, service.grants, service.uploads, service.quarantine
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = nil
	service.grants = nil
	service.uploads = session
	service.quarantine = nil
	service.pendingUploads = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine)
	emitRepositorySnapshot(emitter, projection)
	if show != nil {
		show()
	}

	select {
	case result := <-session.result:
		service.finishUploads(session)
		return result, nil
	case <-ctx.Done():
		service.finishUploads(session)
		return platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}, ctx.Err()
	}
}

func (service *RepositoryService) showQuarantine(ctx context.Context, request platform.QuarantineDialogRequest) (platform.QuarantineDialogResult, error) {
	service.mu.Lock()
	if request.DirectEntry {
		service.pendingQuarantine = repositoryContextKey(request.ServerID, request.RepoID)
		service.snapshot.Context = RepositoryContextProjection{ServerID: request.ServerID, RepoID: request.RepoID, Name: request.RepositoryName}
	}
	contextProjection := service.snapshot.Context
	pending := service.pendingQuarantine
	if pending == "" || pending != repositoryContextKey(contextProjection.ServerID, contextProjection.RepoID) {
		service.mu.Unlock()
		return platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}, nil
	}
	projection, ok := projectQuarantine(request, contextProjection)
	if !ok {
		service.pendingQuarantine = ""
		service.mu.Unlock()
		return platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}, nil
	}
	session := &repositoryQuarantineSession{result: make(chan platform.QuarantineDialogResult, 1)}
	previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine := service.settings, service.shares, service.grants, service.uploads, service.quarantine
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = nil
	service.grants = nil
	service.uploads = nil
	service.quarantine = session
	service.pendingQuarantine = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads, previousQuarantine)
	emitRepositorySnapshot(emitter, projection)
	if show != nil {
		show()
	}

	select {
	case result := <-session.result:
		service.finishQuarantine(session)
		return result, nil
	case <-ctx.Done():
		service.finishQuarantine(session)
		return platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}, ctx.Err()
	}
}

func (service *RepositoryService) finishSettings(session *repositorySettingsSession) {
	service.mu.Lock()
	if service.settings != session {
		service.mu.Unlock()
		return
	}
	service.settings = nil
	hide := service.hide
	service.mu.Unlock()
	if hide != nil {
		hide()
	}
}

func (service *RepositoryService) finishShares(session *repositorySharesSession) {
	service.mu.Lock()
	if service.shares != session {
		service.mu.Unlock()
		return
	}
	service.shares = nil
	hide := service.hide
	service.mu.Unlock()
	if hide != nil {
		hide()
	}
}

func (service *RepositoryService) finishGrants(session *repositoryGrantsSession) {
	service.mu.Lock()
	if service.grants != session {
		service.mu.Unlock()
		return
	}
	service.grants = nil
	hide := service.hide
	service.mu.Unlock()
	if hide != nil {
		hide()
	}
}

func (service *RepositoryService) finishUploads(session *repositoryUploadsSession) {
	service.mu.Lock()
	if service.uploads != session {
		service.mu.Unlock()
		return
	}
	service.uploads = nil
	hide := service.hide
	service.mu.Unlock()
	if hide != nil {
		hide()
	}
}

// showShelf renders one shelf over the channel list. Unlike the other browsers
// it does not take over the pending-context dance: the shelf is opened from
// inside the channel dialog, so the context that got the owner there is
// already the right one and stealing it would lose the way back.
func (service *RepositoryService) showShelf(ctx context.Context, request platform.ShelfDialogRequest) (platform.ShelfDialogResult, error) {
	service.mu.Lock()
	contextProjection := service.snapshot.Context
	if request.ServerID != "" && request.RepoID != "" {
		contextProjection.ServerID, contextProjection.RepoID = request.ServerID, request.RepoID
		if request.RepositoryName != "" {
			contextProjection.Name = request.RepositoryName
		}
	}
	projection, ok := projectShelf(request, contextProjection)
	if !ok {
		service.mu.Unlock()
		return platform.ShelfDialogResult{Action: platform.ShelfDialogClose}, nil
	}
	session := &repositoryShelfSession{result: make(chan platform.ShelfDialogResult, 1)}
	previousShelf := service.shelf
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.shelf = session
	service.pendingShelf = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	if previousShelf != nil && !previousShelf.resolved {
		previousShelf.resolved = true
		previousShelf.result <- platform.ShelfDialogResult{Action: platform.ShelfDialogClose}
	}
	emitRepositorySnapshot(emitter, projection)
	if show != nil {
		show()
	}

	select {
	case result := <-session.result:
		service.finishShelf(session)
		return result, nil
	case <-ctx.Done():
		service.finishShelf(session)
		return platform.ShelfDialogResult{Action: platform.ShelfDialogClose}, ctx.Err()
	}
}

func (service *RepositoryService) finishShelf(session *repositoryShelfSession) {
	service.mu.Lock()
	if service.shelf != session {
		service.mu.Unlock()
		return
	}
	service.shelf = nil
	service.mu.Unlock()
}

func (adapter repositoryShelfBrowserAdapter) ShowShelf(ctx context.Context, request platform.ShelfDialogRequest) (platform.ShelfDialogResult, error) {
	return adapter.service.showShelf(ctx, request)
}

func (service *RepositoryService) finishQuarantine(session *repositoryQuarantineSession) {
	service.mu.Lock()
	if service.quarantine != session {
		service.mu.Unlock()
		return
	}
	service.quarantine = nil
	hide := service.hide
	service.mu.Unlock()
	if hide != nil {
		hide()
	}
}

func projectRepositorySettings(request platform.SettingsDialogRequest) (RepositorySnapshot, bool) {
	if request.FocusRepoID == "" || len(request.Servers) != 1 {
		return RepositorySnapshot{}, false
	}
	server := request.Servers[0]
	var folder *platform.SettingsFolder
	for i := range server.Folders {
		if server.Folders[i].ID == request.FocusRepoID {
			item := server.Folders[i]
			folder = &item
			break
		}
	}
	if folder == nil || strings.TrimSpace(server.ID) == "" {
		return RepositorySnapshot{}, false
	}
	snapshot := RepositorySnapshot{
		Mode: "actions", Title: request.Title, Text: request.Text, TextKey: request.TextKey,
		Context: RepositoryContextProjection{LastCommitAt: folder.LastCommitAt, CanFoldInactive: folder.CanFoldInactive, ServerID: server.ID, ServerName: server.Name, Address: server.Address, Realm: server.Realm, RepoID: folder.ID, Name: folder.Name, LocalPath: folder.LocalPath, State: folder.State, Access: folder.Access, Editing: folder.Editing, StateKey: folder.StateKey, AccessKey: folder.AccessKey, EditingKey: folder.EditingKey},
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{}, Uploads: []UploadChannelProjection{},
	}
	if folder.CanManageGrants {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogManageGrants), LabelKey: "repoAction.manage_grants.label", DescriptionKey: "repoAction.manage_grants.description", Tone: "primary"})
	}
	if folder.CanManagePublicShares {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogPublicShares), LabelKey: "repoAction.public_shares.label", DescriptionKey: "repoAction.public_shares.description", Tone: "primary"})
	}
	if folder.CanManageUploadChannels {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogUploadChannels), LabelKey: "repoAction.upload_channels.label", DescriptionKey: "repoAction.upload_channels.description", Tone: "primary"})
	}
	if folder.CanReviewQuarantine {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogQuarantine), LabelKey: "repoAction.quarantine.label", DescriptionKey: "repoAction.quarantine.description", Tone: "primary"})
	}
	if folder.CanBrowseHistory {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogBrowseHistory), LabelKey: "repoAction.browse_history.label", DescriptionKey: "repoAction.browse_history.description", Tone: "primary"})
	}
	if folder.CanSetEditingPolicy {
		label := "repoAction.enable_editing_lock.label"
		description := "repoAction.enable_editing_lock.description"
		if folder.LockRequired {
			label = "repoAction.disable_editing_lock.label"
			description = "repoAction.disable_editing_lock.description"
		}
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogEditingPolicy), LabelKey: label, DescriptionKey: description, Tone: "warning"})
	}
	if folder.CanConnect {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogConnectRepos), LabelKey: "repoAction.connect_repositories.label", DescriptionKey: "repoAction.connect_repositories.description", Tone: "primary"})
	}
	if folder.CanLocate {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogLocateFolder), LabelKey: "repoAction.locate_folder.label", DescriptionKey: "repoAction.locate_folder.description", Tone: "warning"})
	}
	if folder.CanMove {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogMoveFolder), LabelKey: "repoAction.move_folder.label", DescriptionKey: "repoAction.move_folder.description", Tone: "primary"})
	}
	if folder.CanRetryLifecycle {
		// Lifecycle repair is separate from interpreting an unscheduled rename.
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogRetryLifecycle), LabelKey: "repoAction.retry_lifecycle.label", DescriptionKey: "repoAction.retry_lifecycle.description", Tone: "primary"})
	}
	if folder.CanAbandonLifecycle {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogAbandonLifecycle), LabelKey: "repoAction.abandon_lifecycle.label", DescriptionKey: "repoAction.abandon_lifecycle.description", Tone: "warning"})
	}
	if folder.CanResolveIntents {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogResolveIntents), LabelKey: "repoAction.resolve_intents.label", DescriptionKey: "repoAction.resolve_intents.description", Tone: "warning"})
	}
	if folder.CanResolveCommitRecovery {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogResolveCommitRecovery), LabelKey: "repoAction.resolve_commit_recovery.label", DescriptionKey: "repoAction.resolve_commit_recovery.description", Tone: "warning"})
	}
	if folder.CanDetach {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogDetachFolder), LabelKey: "repoAction.detach_folder.label", DescriptionKey: "repoAction.detach_folder.description", Tone: "warning"})
	}
	if folder.CanDelete {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogDeleteRepo), LabelKey: "repoAction.delete_repository.label", DescriptionKey: "repoAction.delete_repository.description", Tone: "danger"})
	}
	if folder.CanLoadDump {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogLoadDump), LabelKey: "repoAction.load_dump.label", DescriptionKey: "repoAction.load_dump.description", Tone: "warning"})
	}
	return snapshot, strings.TrimSpace(snapshot.Context.RepoID) != ""
}

func projectPublicShares(request platform.PublicShareDialogRequest) (RepositorySnapshot, bool) {
	if strings.TrimSpace(request.ServerID) == "" || strings.TrimSpace(request.RepoID) == "" {
		return RepositorySnapshot{}, false
	}
	snapshot := RepositorySnapshot{
		Mode: "shares", Title: request.Title, Text: request.Text, TextKey: request.TextKey,
		FocusChannelID: request.FocusChannelID,
		Context:        RepositoryContextProjection{ServerID: request.ServerID, RepoID: request.RepoID, Name: request.RepositoryName},
		Actions:        []RepositoryActionProjection{}, Shares: make([]PublicShareProjection, 0, len(request.Shares)), Grants: []RealmGrantProjection{}, Uploads: []UploadChannelProjection{},
	}
	for _, share := range request.Shares {
		active := strings.EqualFold(strings.TrimSpace(share.State), "aktywne") || strings.EqualFold(strings.TrimSpace(share.State), "active")
		snapshot.Shares = append(snapshot.Shares, PublicShareProjection{
			ChannelID: share.ChannelID, Address: share.Address, State: share.State, StateKey: share.StateKey, SourceRoot: share.SourceRoot,
			Recipients: share.Recipients, Password: share.Password, Revision: share.Revision,
			CanEdit: active, CanRevoke: active, CanDelete: strings.TrimSpace(share.ChannelID) != "",
		})
	}
	return snapshot, true
}

func projectRealmGrants(request platform.RealmGrantDialogRequest, contextProjection RepositoryContextProjection) (RepositorySnapshot, bool) {
	if strings.TrimSpace(contextProjection.ServerID) == "" || strings.TrimSpace(contextProjection.RepoID) == "" || len(request.Recipients) == 0 {
		return RepositorySnapshot{}, false
	}
	snapshot := RepositorySnapshot{
		Mode: "grants", Title: request.Title, Text: request.Text, TextKey: request.TextKey, Context: contextProjection,
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: make([]RealmGrantProjection, 0, len(request.Recipients)), Uploads: []UploadChannelProjection{},
	}
	for _, recipient := range request.Recipients {
		realmID := strings.TrimSpace(recipient.RealmID)
		if realmID == "" {
			continue
		}
		active := strings.EqualFold(strings.TrimSpace(recipient.State), "active") || strings.EqualFold(strings.TrimSpace(recipient.State), "aktywne")
		access := strings.TrimSpace(recipient.Access)
		snapshot.Grants = append(snapshot.Grants, RealmGrantProjection{
			RealmID: realmID, Alias: recipient.Alias, Access: access, State: recipient.State,
			CanRead: !active || access != "r", CanWrite: !active || access != "rw", CanRevoke: active,
		})
	}
	return snapshot, len(snapshot.Grants) > 0
}

func projectUploadChannels(request platform.UploadChannelDialogRequest, contextProjection RepositoryContextProjection) (RepositorySnapshot, bool) {
	if strings.TrimSpace(contextProjection.ServerID) == "" || strings.TrimSpace(contextProjection.RepoID) == "" {
		return RepositorySnapshot{}, false
	}
	snapshot := RepositorySnapshot{
		Mode: "uploads", Title: request.Title, Text: request.Text, TextKey: request.TextKey, Context: contextProjection,
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{}, Uploads: make([]UploadChannelProjection, 0, len(request.Channels)), Quarantine: []QuarantineItemProjection{},
	}
	for _, channel := range request.Channels {
		channelID := strings.TrimSpace(channel.ChannelID)
		if channelID == "" {
			continue
		}
		active := strings.EqualFold(strings.TrimSpace(channel.State), "aktywne") || strings.EqualFold(strings.TrimSpace(channel.State), "active")
		snapshot.Uploads = append(snapshot.Uploads, UploadChannelProjection{
			ChannelID: channelID, Address: channel.Address, State: channel.State, StateKey: channel.StateKey, Recipients: channel.Recipients,
			RequireOTP: channel.RequireOTP, CanEdit: active, CanRevoke: active, CanDelete: true,
		})
	}
	return snapshot, true
}

func projectShelf(request platform.ShelfDialogRequest, contextProjection RepositoryContextProjection) (RepositorySnapshot, bool) {
	if strings.TrimSpace(contextProjection.ServerID) == "" || strings.TrimSpace(contextProjection.RepoID) == "" {
		return RepositorySnapshot{}, false
	}
	if strings.TrimSpace(request.ChannelID) == "" {
		return RepositorySnapshot{}, false
	}
	snapshot := RepositorySnapshot{
		Mode: "shelf", Title: request.Title, Text: request.Text, TextKey: request.TextKey, TextPrefix: request.TextPrefix,
		Context: contextProjection, ShelfName: request.ShelfName, FocusChannelID: request.ChannelID, ShelfCanImport: request.CanImport,
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{},
		Uploads: []UploadChannelProjection{}, Quarantine: []QuarantineItemProjection{},
		Shelf: make([]ShelfItemProjection, 0, len(request.Items)),
	}
	for _, item := range request.Items {
		uploadID := strings.TrimSpace(item.UploadID)
		if uploadID == "" {
			continue
		}
		// The contributor's own name is what the owner recognises, so it is
		// shown; the repository path is what a fetch will ask for, so it is
		// carried too. Neither substitutes for the other.
		name := strings.TrimSpace(item.OriginalName)
		if name == "" {
			name = strings.TrimSpace(item.RepoPath)
		}
		if name == "" {
			name = uploadID
		}
		snapshot.Shelf = append(snapshot.Shelf, ShelfItemProjection{
			UploadID: uploadID, RepoPath: item.RepoPath, OriginalName: name,
			Size: item.Size, SizeLabel: quarantineSizeLabel(item.Size),
			Revision: item.Revision, AcceptedAt: item.AcceptedAt,
		})
	}
	return snapshot, true
}

func projectQuarantine(request platform.QuarantineDialogRequest, contextProjection RepositoryContextProjection) (RepositorySnapshot, bool) {
	if strings.TrimSpace(contextProjection.ServerID) == "" || strings.TrimSpace(contextProjection.RepoID) == "" {
		return RepositorySnapshot{}, false
	}
	snapshot := RepositorySnapshot{
		Mode: "quarantine", Title: request.Title, Text: request.Text, TextKey: request.TextKey, TextPrefix: request.TextPrefix, Context: contextProjection,
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{}, Uploads: []UploadChannelProjection{},
		Quarantine: make([]QuarantineItemProjection, 0, len(request.Items)),
	}
	for _, item := range request.Items {
		uploadID := strings.TrimSpace(item.UploadID)
		if uploadID == "" {
			continue
		}
		name := strings.TrimSpace(item.OriginalName)
		if name == "" {
			name = uploadID
		}
		snapshot.Quarantine = append(snapshot.Quarantine, QuarantineItemProjection{
			UploadID: uploadID, OriginalName: name, Size: item.Size, SizeLabel: quarantineSizeLabel(item.Size),
			AVVerdict: item.AVVerdict, RemainingHours: item.RemainingHours,
		})
	}
	return snapshot, true
}

func quarantineSizeLabel(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	default:
		return fmt.Sprintf("%d MB", (n+1024*1024-1)/(1024*1024))
	}
}

func closePreviousRepositorySessions(settings *repositorySettingsSession, shares *repositorySharesSession, grants *repositoryGrantsSession, uploads *repositoryUploadsSession, quarantine *repositoryQuarantineSession) {
	if settings != nil && !settings.resolved {
		settings.resolved = true
		settings.result <- platform.SettingsDialogResult{Action: platform.SettingsDialogClose}
	}
	if shares != nil && !shares.resolved {
		shares.resolved = true
		shares.result <- platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}
	}
	if grants != nil && !grants.resolved {
		grants.resolved = true
		grants.result <- platform.RealmGrantDialogResult{Action: platform.RealmGrantDialogClose}
	}
	if uploads != nil && !uploads.resolved {
		uploads.resolved = true
		uploads.result <- platform.UploadChannelDialogResult{Action: platform.UploadChannelDialogClose}
	}
	if quarantine != nil && !quarantine.resolved {
		quarantine.resolved = true
		quarantine.result <- platform.QuarantineDialogResult{Action: platform.QuarantineDialogClose}
	}
}

func emitRepositorySnapshot(emitter snapshotEmitter, snapshot RepositorySnapshot) {
	if emitter != nil {
		emitter.Emit(repositorySnapshotEvent, snapshot)
	}
}

func trimRepositoryChoice(choice RepositoryChoice) RepositoryChoice {
	choice.Action = strings.TrimSpace(choice.Action)
	choice.ServerID = strings.TrimSpace(choice.ServerID)
	choice.RepoID = strings.TrimSpace(choice.RepoID)
	choice.ChannelID = strings.TrimSpace(choice.ChannelID)
	choice.RealmID = strings.TrimSpace(choice.RealmID)
	choice.UploadID = strings.TrimSpace(choice.UploadID)
	return choice
}

func quarantineChoiceAllowed(items []QuarantineItemProjection, action platform.QuarantineDialogAction, uploadID string) bool {
	if uploadID == "" || (action != platform.QuarantineDialogFetch && action != platform.QuarantineDialogHide) {
		return false
	}
	for _, item := range items {
		if item.UploadID == uploadID {
			return true
		}
	}
	return false
}

func grantChoiceAllowed(grants []RealmGrantProjection, action platform.RealmGrantDialogAction, realmID string) bool {
	for _, grant := range grants {
		if grant.RealmID != realmID || realmID == "" {
			continue
		}
		switch action {
		case platform.RealmGrantDialogRead:
			return grant.CanRead
		case platform.RealmGrantDialogWrite:
			return grant.CanWrite
		case platform.RealmGrantDialogRevoke:
			return grant.CanRevoke
		}
	}
	return false
}

func uploadChoiceAllowed(channels []UploadChannelProjection, action platform.UploadChannelDialogAction, channelID string) bool {
	if action == platform.UploadChannelDialogCreate {
		return channelID == ""
	}
	for _, channel := range channels {
		if channel.ChannelID != channelID || channelID == "" {
			continue
		}
		switch action {
		case platform.UploadChannelDialogBrowse:
			return true
		case platform.UploadChannelDialogEdit:
			return channel.CanEdit
		case platform.UploadChannelDialogRevoke:
			return channel.CanRevoke
		case platform.UploadChannelDialogDelete:
			return channel.CanDelete
		}
	}
	return false
}

func sameRepositoryContext(snapshot RepositorySnapshot, choice RepositoryChoice) bool {
	return choice.ServerID != "" && choice.RepoID != "" && choice.ServerID == snapshot.Context.ServerID && choice.RepoID == snapshot.Context.RepoID
}

func repositoryContextKey(serverID, repoID string) string {
	return strings.TrimSpace(serverID) + "\x00" + strings.TrimSpace(repoID)
}

func projectedRepositoryAction(actions []RepositoryActionProjection, id string) (RepositoryActionProjection, bool) {
	for _, action := range actions {
		if action.ID == id {
			return action, true
		}
	}
	return RepositoryActionProjection{}, false
}

func shareChoiceAllowed(shares []PublicShareProjection, action platform.PublicShareDialogAction, channelID string) bool {
	if action == platform.PublicShareDialogCreate {
		return channelID == ""
	}
	for _, share := range shares {
		if share.ChannelID != channelID || channelID == "" {
			continue
		}
		switch action {
		case platform.PublicShareDialogEdit:
			return share.CanEdit
		case platform.PublicShareDialogRevoke:
			return share.CanRevoke
		case platform.PublicShareDialogDelete:
			return share.CanDelete
		}
	}
	return false
}
