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
	// pendingShares binds a controller continuation to the exact repository
	// action that requested it. A newer gear click clears the continuation.
	pendingShares string
	revision      uint64
}

type repositorySettingsSession struct {
	result   chan platform.SettingsDialogResult
	resolved bool
}

type repositorySharesSession struct {
	result   chan platform.PublicShareDialogResult
	resolved bool
}

type repositorySettingsBrowserAdapter struct{ service *RepositoryService }
type repositoryPublicShareBrowserAdapter struct{ service *RepositoryService }

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

type RepositoryChoice struct {
	Action    string `json:"action"`
	ServerID  string `json:"server_id"`
	RepoID    string `json:"repo_id"`
	ChannelID string `json:"channel_id,omitempty"`
}

type RepositoryAcceptance struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
}

func newRepositoryService() *RepositoryService {
	return &RepositoryService{snapshot: emptyRepositorySnapshot()}
}

func emptyRepositorySnapshot() RepositorySnapshot {
	return RepositorySnapshot{Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{}}
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
	hide := service.hide
	resolveSettings := settings != nil && !settings.resolved
	resolveShares := shares != nil && !shares.resolved
	if resolveSettings {
		settings.resolved = true
	}
	if resolveShares {
		shares.resolved = true
	}
	service.pendingShares = ""
	service.mu.Unlock()
	if resolveSettings {
		settings.result <- platform.SettingsDialogResult{Action: platform.SettingsDialogClose}
	}
	if resolveShares {
		shares.result <- platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}
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

func (service *RepositoryService) showSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	projection, ok := projectRepositorySettings(request)
	if !ok {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
	}
	session := &repositorySettingsSession{result: make(chan platform.SettingsDialogResult, 1)}

	service.mu.Lock()
	previousSettings, previousShares := service.settings, service.shares
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = session
	service.shares = nil
	service.pendingShares = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares)
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
	previousSettings, previousShares := service.settings, service.shares
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.settings = nil
	service.shares = session
	service.pendingShares = ""
	emitter, show := service.emitter, service.show
	service.mu.Unlock()
	closePreviousRepositorySessions(previousSettings, previousShares)
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
		Actions: []RepositoryActionProjection{}, Shares: []PublicShareProjection{},
	}
	if folder.CanManagePublicShares {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogPublicShares), Label: "Udostępnienia publiczne", Description: "Publikuj wybrane pliki i zarządzaj aktywnymi adresami.", Tone: "primary"})
	}
	if folder.CanConnect {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogConnectRepos), Label: "Połącz folder", Description: "Wybierz lokalne miejsce i rozpocznij pierwszy checkout.", Tone: "primary"})
	}
	if folder.CanDetach {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogDetachFolder), Label: "Odłącz folder", Description: "Zatrzymaj synchronizację, pozostawiając pliki na dysku.", Tone: "warning"})
	}
	if folder.CanDelete {
		snapshot.Actions = append(snapshot.Actions, RepositoryActionProjection{ID: string(platform.SettingsDialogDeleteRepo), Label: "Usuń repozytorium", Description: "Usuń historię serwerową i odłącz lokalny folder.", Tone: "danger"})
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
		Actions: []RepositoryActionProjection{}, Shares: make([]PublicShareProjection, 0, len(request.Shares)),
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

func closePreviousRepositorySessions(settings *repositorySettingsSession, shares *repositorySharesSession) {
	if settings != nil && !settings.resolved {
		settings.resolved = true
		settings.result <- platform.SettingsDialogResult{Action: platform.SettingsDialogClose}
	}
	if shares != nil && !shares.resolved {
		shares.resolved = true
		shares.result <- platform.PublicShareDialogResult{Action: platform.PublicShareDialogClose}
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
	return choice
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
