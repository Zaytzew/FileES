package main

import (
	"context"
	"strings"
	"sync"

	"filees/internal/gui/platform"
)

const repositorySnapshotEvent = "filees:repository-snapshot"

// RepositoryService owns the presentation session for one repository window.
// It receives already-authorised models from the shared controller and returns
// only opaque IDs plus closed action enums. It never calls daemon IPC.
type RepositoryService struct {
	mu       sync.RWMutex
	snapshot RepositorySnapshot
	emitter  snapshotEmitter
	show     func()
	hide     func()
	settings *repositorySettingsSession
	shares   *repositorySharesSession
	grants   *repositoryGrantsSession
	uploads  *repositoryUploadsSession
	// pendingShares binds a controller continuation to the exact repository
	// action that requested it. A newer gear click clears the continuation.
	pendingShares  string
	pendingGrants  string
	pendingUploads string
	revision       uint64
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

type repositorySettingsBrowserAdapter struct{ service *RepositoryService }
type repositoryPublicShareBrowserAdapter struct{ service *RepositoryService }
type repositoryRealmGrantBrowserAdapter struct {
	service  *RepositoryService
	fallback platform.RealmGrantBrowser
}
type repositoryUploadChannelBrowserAdapter struct{ service *RepositoryService }

type settingsBrowserRouter struct {
	server     settingsBrowserAdapter
	repository repositorySettingsBrowserAdapter
}

type RepositorySnapshot struct {
	Revision uint64                       `json:"revision"`
	Mode     string                       `json:"mode"`
	Title    string                       `json:"title"`
	Text     string                       `json:"text"`
	Busy     bool                         `json:"busy"`
	Context  RepositoryContextProjection  `json:"context"`
	Actions  []RepositoryActionProjection `json:"actions"`
	Shares   []PublicShareProjection      `json:"shares"`
	Grants   []RealmGrantProjection       `json:"grants"`
	Uploads  []UploadChannelProjection    `json:"uploads"`
}

type RepositoryContextProjection struct {
	ServerID   string `json:"server_id"`
	ServerName string `json:"server_name"`
	Address    string `json:"address"`
	Realm      string `json:"realm"`
	RepoID     string `json:"repo_id"`
	Name       string `json:"name"`
	LocalPath  string `json:"local_path"`
	State      string `json:"state"`
	Access     string `json:"access"`
	Editing    string `json:"editing"`
}

type RepositoryActionProjection struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Tone        string `json:"tone"`
}

type PublicShareProjection struct {
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
	ChannelID  string `json:"channel_id"`
	Address    string `json:"address"`
	State      string `json:"state"`
	Recipients string `json:"recipients"`
	RequireOTP bool   `json:"require_otp,omitempty"`
	CanEdit    bool   `json:"can_edit"`
	CanRevoke  bool   `json:"can_revoke"`
	CanDelete  bool   `json:"can_delete"`
}

type RepositoryChoice struct {
	Action    string `json:"action"`
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	ChannelID string `json:"channel_id,omitempty"`
	RealmID   string `json:"realm_id,omitempty"`
}

type RepositoryAcceptance struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
}

func newRepositoryService() *RepositoryService {
	return &RepositoryService{snapshot: emptyRepositorySnapshot()}
}

func emptyRepositorySnapshot() RepositorySnapshot {
	return RepositorySnapshot{Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{}, Uploads: []UploadChannelProjection{}}
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
	hide := service.hide
	resolveSettings := settings != nil && !settings.resolved
	resolveShares := shares != nil && !shares.resolved
	resolveGrants := grants != nil && !grants.resolved
	resolveUploads := uploads != nil && !uploads.resolved
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
	service.pendingShares = ""
	service.pendingGrants = ""
	service.pendingUploads = ""
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
	if adapter.fallback == nil {
		return platform.RealmVisibilityDialogResult{Action: platform.RealmVisibilityDialogClose}, nil
	}
	return adapter.fallback.ShowRealmVisibility(ctx, request)
}

func (adapter repositoryUploadChannelBrowserAdapter) ShowUploadChannels(ctx context.Context, request platform.UploadChannelDialogRequest) (platform.UploadChannelDialogResult, error) {
	return adapter.service.showUploadChannels(ctx, request)
}

func (service *RepositoryService) showSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	projection, ok := projectRepositorySettings(request)
	if !ok {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
	}
	session := &repositorySettingsSession{result: make(chan platform.SettingsDialogResult, 1)}

	service.mu.Lock()
	previousSettings, previousShares, previousGrants, previousUploads := service.settings, service.shares, service.grants, service.uploads
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = session
	service.shares = nil
	service.grants = nil
	service.uploads = nil
	service.pendingShares = ""
	service.pendingGrants = ""
	service.pendingUploads = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads)
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
	if service.pendingShares != repositoryContextKey(request.ServerID, request.RepoID) {
		service.mu.Unlock()
		return platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}, nil
	}
	previousSettings, previousShares, previousGrants, previousUploads := service.settings, service.shares, service.grants, service.uploads
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = session
	service.grants = nil
	service.uploads = nil
	service.pendingShares = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads)
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
	previousSettings, previousShares, previousGrants, previousUploads := service.settings, service.shares, service.grants, service.uploads
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = nil
	service.grants = session
	service.uploads = nil
	service.pendingGrants = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads)
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
	previousSettings, previousShares, previousGrants, previousUploads := service.settings, service.shares, service.grants, service.uploads
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = nil
	service.grants = nil
	service.uploads = session
	service.pendingUploads = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares, previousGrants, previousUploads)
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
		Mode: "actions", Title: request.Title, Text: request.Text,
		Context: RepositoryContextProjection{ServerID: server.ID, ServerName: server.Name, Address: server.Address, Realm: server.Realm, RepoID: folder.ID, Name: folder.Name, LocalPath: folder.LocalPath, State: folder.State, Access: folder.Access, Editing: folder.Editing},
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{}, Uploads: []UploadChannelProjection{},
	}
	if folder.CanManageGrants {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogManageGrants), Label: "Uprawnienia gości", Description: "Nadaj albo cofnij dostęp widocznym strefom FileES.", Tone: "primary"})
	}
	if folder.CanManagePublicShares {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogPublicShares), Label: "Udostępnienia publiczne", Description: "Publikuj wybrane pliki i zarządzaj aktywnymi adresami.", Tone: "primary"})
	}
	if folder.CanManageUploadChannels {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogUploadChannels), Label: "Półki przyjęcia", Description: "Przyjmuj pliki do repozytorium przez zamknięty kanał przeglądarkowy.", Tone: "primary"})
	}
	if folder.CanSetEditingPolicy {
		label := "Włącz wypożyczanie plików"
		description := "Wymagaj blokady przed edycją każdego pliku w repozytorium."
		if folder.LockRequired {
			label = "Wyłącz wypożyczanie plików"
			description = "Przywróć swobodną edycję bez obowiązkowej blokady."
		}
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogEditingPolicy), Label: label, Description: description, Tone: "warning"})
	}
	if folder.CanConnect {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogConnectRepos), Label: "Połącz folder", Description: "Wybierz lokalne miejsce i rozpocznij pierwszy checkout.", Tone: "primary"})
	}
	if folder.CanLocate {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogLocateFolder), Label: "Wskaż przeniesiony folder", Description: "Powiąż repozytorium z istniejącą kopią roboczą w nowym miejscu.", Tone: "warning"})
	}
	if folder.CanDetach {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogDetachFolder), Label: "Odłącz folder", Description: "Zatrzymaj synchronizację, pozostawiając pliki na dysku.", Tone: "warning"})
	}
	if folder.CanDelete {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogDeleteRepo), Label: "Usuń repozytorium", Description: "Usuń historię serwerową i odłącz lokalny folder.", Tone: "danger"})
	}
	if folder.CanLoadDump {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogLoadDump), Label: "Odtwórz z archiwum", Description: "Zaimportuj historię z archiwum umieszczonego w folderze roboczym.", Tone: "warning"})
	}
	return snapshot, strings.TrimSpace(snapshot.Context.RepoID) != ""
}

func projectPublicShares(request platform.PublicShareDialogRequest) (RepositorySnapshot, bool) {
	if strings.TrimSpace(request.ServerID) == "" || strings.TrimSpace(request.RepoID) == "" {
		return RepositorySnapshot{}, false
	}
	snapshot := RepositorySnapshot{
		Mode: "shares", Title: request.Title, Text: request.Text,
		Context: RepositoryContextProjection{ServerID: request.ServerID, RepoID: request.RepoID, Name: request.RepositoryName},
		Actions: []RepositoryActionProjection{}, Shares: make([]PublicShareProjection, 0, len(request.Shares)), Grants: []RealmGrantProjection{}, Uploads: []UploadChannelProjection{},
	}
	for _, share := range request.Shares {
		active := strings.EqualFold(strings.TrimSpace(share.State), "aktywne") || strings.EqualFold(strings.TrimSpace(share.State), "active")
		snapshot.Shares = append(snapshot.Shares, PublicShareProjection{
			ChannelID: share.ChannelID, Address: share.Address, State: share.State, SourceRoot: share.SourceRoot,
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
		Mode: "grants", Title: request.Title, Text: request.Text, Context: contextProjection,
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
		Mode: "uploads", Title: request.Title, Text: request.Text, Context: contextProjection,
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}, Grants: []RealmGrantProjection{}, Uploads: make([]UploadChannelProjection, 0, len(request.Channels)),
	}
	for _, channel := range request.Channels {
		channelID := strings.TrimSpace(channel.ChannelID)
		if channelID == "" {
			continue
		}
		active := strings.EqualFold(strings.TrimSpace(channel.State), "aktywne") || strings.EqualFold(strings.TrimSpace(channel.State), "active")
		snapshot.Uploads = append(snapshot.Uploads, UploadChannelProjection{
			ChannelID: channelID, Address: channel.Address, State: channel.State, Recipients: channel.Recipients,
			RequireOTP: channel.RequireOTP, CanEdit: active, CanRevoke: active, CanDelete: true,
		})
	}
	return snapshot, true
}

func closePreviousRepositorySessions(settings *repositorySettingsSession, shares *repositorySharesSession, grants *repositoryGrantsSession, uploads *repositoryUploadsSession) {
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
	return choice
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
