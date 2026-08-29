package main

import (
	"context"
	"strings"
	"sync"

	"filees/internal/gui/platform"
)

const settingsSnapshotEvent = "filees:settings-snapshot"

// SettingsService is the browser-facing half of the Wails settings window.
// It only projects choices already authorised by the shared action model and
// returns opaque IDs to the controller. It never calls daemon IPC itself.
type SettingsService struct {
	mu       sync.RWMutex
	snapshot SettingsSnapshot
	emitter  snapshotEmitter
	show     func()
	hide     func()
	active   *settingsSession
	revision uint64
}

type settingsSession struct {
	result   chan platform.SettingsDialogResult
	resolved bool
}

type settingsBrowserAdapter struct{ service *SettingsService }

type SettingsSnapshot struct {
	Revision uint64                   `json:"revision"`
	Title    string                   `json:"title"`
	Text     string                   `json:"text"`
	Server   SettingsServerProjection `json:"server"`
}

type SettingsServerProjection struct {
	ID                   string                     `json:"id"`
	Name                 string                     `json:"name"`
	Address              string                     `json:"address"`
	Realm                string                     `json:"realm"`
	ClientID             string                     `json:"client_id"`
	SessionTimeoutMin    int                        `json:"session_timeout_min"`
	CanSetSessionTimeout bool                       `json:"can_set_session_timeout"`
	Actions              []SettingsActionProjection `json:"actions"`
}

type SettingsActionProjection struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Tone        string `json:"tone"`
}

type SettingsChoice struct {
	Action   string `json:"action"`
	ServerID string `json:"server_id"`
}

type SettingsAcceptance struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
}

func newSettingsService() *SettingsService {
	return &SettingsService{snapshot: SettingsSnapshot{Server: SettingsServerProjection{Actions: []SettingsActionProjection{}}}}
}

func (service *SettingsService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

func (service *SettingsService) attachPresentation(show, hide func()) {
	service.mu.Lock()
	service.show = show
	service.hide = hide
	service.mu.Unlock()
}

// Snapshot returns the last immutable settings projection. A hidden window
// may call it while loading; Revision zero means there is no active context.
func (service *SettingsService) Snapshot() SettingsSnapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.snapshot
}

// Choose accepts only actions exposed by the current server-scoped model.
// Acceptance means the choice reached the shared controller, not that the
// subsequent daemon mutation succeeded.
func (service *SettingsService) Choose(choice SettingsChoice) SettingsAcceptance {
	choice.Action = strings.TrimSpace(choice.Action)
	choice.ServerID = strings.TrimSpace(choice.ServerID)

	service.mu.Lock()
	session := service.active
	snapshot := service.snapshot
	hide := service.hide
	if session == nil {
		service.mu.Unlock()
		return SettingsAcceptance{Code: "settings_inactive"}
	}
	if choice.ServerID == "" || choice.ServerID != snapshot.Server.ID {
		service.mu.Unlock()
		return SettingsAcceptance{Code: "settings_context_changed"}
	}
	allowed := choice.Action == string(platform.SettingsDialogSessionTimeout) && snapshot.Server.CanSetSessionTimeout
	if !allowed {
		for _, action := range snapshot.Server.Actions {
			if action.ID == choice.Action {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		service.mu.Unlock()
		return SettingsAcceptance{Code: "settings_action_unavailable"}
	}
	if session.resolved {
		service.mu.Unlock()
		return SettingsAcceptance{Code: "settings_choice_busy"}
	}
	session.resolved = true
	service.mu.Unlock()

	result := platform.SettingsDialogResult{Action: platform.SettingsDialogAction(choice.Action), ServerID: choice.ServerID}
	session.result <- result
	if hide != nil {
		hide()
	}
	return SettingsAcceptance{Accepted: true}
}

// Cancel closes only the presentation session. It cannot stop FileES or
// mutate server state.
func (service *SettingsService) Cancel() {
	service.mu.Lock()
	session := service.active
	hide := service.hide
	shouldResolve := session != nil && !session.resolved
	if shouldResolve {
		session.resolved = true
	}
	service.mu.Unlock()
	if shouldResolve {
		session.result <- platform.SettingsDialogResult{Action: platform.SettingsDialogClose}
	}
	if hide != nil {
		hide()
	}
}

func (adapter settingsBrowserAdapter) ShowSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	return adapter.service.showSettings(ctx, request)
}

func (service *SettingsService) showSettings(ctx context.Context, request platform.SettingsDialogRequest) (platform.SettingsDialogResult, error) {
	projection, ok := projectSettingsRequest(request)
	if !ok {
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, nil
	}
	session := &settingsSession{result: make(chan platform.SettingsDialogResult, 1)}

	service.mu.Lock()
	previous := service.active
	service.revision++
	projection.Revision = service.revision
	service.snapshot = projection
	service.active = session
	emitter := service.emitter
	show := service.show
	service.mu.Unlock()

	if previous != nil {
		select {
		case previous.result <- platform.SettingsDialogResult{Action: platform.SettingsDialogClose}:
		default:
		}
	}
	if emitter != nil {
		emitter.Emit(settingsSnapshotEvent, projection)
	}
	if show != nil {
		show()
	}

	select {
	case result := <-session.result:
		service.finishSession(session)
		return result, nil
	case <-ctx.Done():
		service.finishSession(session)
		return platform.SettingsDialogResult{Action: platform.SettingsDialogClose}, ctx.Err()
	}
}

func (service *SettingsService) finishSession(session *settingsSession) {
	service.mu.Lock()
	if service.active != session {
		service.mu.Unlock()
		return
	}
	service.active = nil
	hide := service.hide
	service.mu.Unlock()
	if hide != nil {
		hide()
	}
}

func projectSettingsRequest(request platform.SettingsDialogRequest) (SettingsSnapshot, bool) {
	wizard := platform.BuildSettingsWizard(request)
	if len(request.Servers) != 1 || len(wizard.Servers) != 1 {
		return SettingsSnapshot{}, false
	}
	server := request.Servers[0]
	wizardServer := wizard.Servers[0]
	projection := SettingsSnapshot{
		Title: request.Title,
		Text:  request.Text,
		Server: SettingsServerProjection{
			ID: server.ID, Name: server.Name, Address: server.Address, Realm: server.Realm, ClientID: server.ClientID,
			SessionTimeoutMin: server.SessionTimeoutMin, CanSetSessionTimeout: server.CanSetSessionTimeout,
			Actions: []SettingsActionProjection{},
		},
	}
	for _, action := range wizardServer.Actions {
		if action.ID == string(platform.SettingsDialogSessionTimeout) || action.ID == string(platform.SettingsDialogPairMobile) || action.NeedsFolder {
			continue
		}
		projection.Server.Actions = append(projection.Server.Actions, projectSettingsAction(action))
	}
	return projection, strings.TrimSpace(projection.Server.ID) != ""
}

func projectSettingsAction(action platform.SettingsWizardAction) SettingsActionProjection {
	projection := SettingsActionProjection{ID: action.ID, Label: action.Label, Description: settingsActionDescription(action.Action), Tone: "primary"}
	switch action.Action {
	case platform.SettingsDialogDetachFolder, platform.SettingsDialogDetachServer:
		projection.Tone = "warning"
	case platform.SettingsDialogDeleteRepo, platform.SettingsDialogRemoveRealm:
		projection.Tone = "danger"
	}
	return projection
}

func settingsActionDescription(action platform.SettingsDialogAction) string {
	switch action {
	case platform.SettingsDialogRealmVisibility:
		return "Zdecyduj, czy inne strefy mogą wskazać tę strefę jako odbiorcę grantu."
	case platform.SettingsDialogRealmBranding:
		return "Ustaw nazwę, kolory i znak prezentowany odbiorcom linków."
	case platform.SettingsDialogRealmAlias:
		return "Nadaj strefie niezmienny pseudonim używany przy blokadach i współdzieleniu."
	case platform.SettingsDialogPairMobile:
		return "Wygeneruj bezpieczny kod QR dla aplikacji FileES na telefonie."
	case platform.SettingsDialogAddFolder:
		return "Utwórz repozytorium z wybranego lokalnego folderu."
	case platform.SettingsDialogConnectRepos:
		return "Połącz tę instalację z istniejącym repozytorium strefy."
	case platform.SettingsDialogLocateFolder:
		return "Wskaż przeniesioną kopię roboczą zawierającą pasujące .svn."
	case platform.SettingsDialogManageGrants:
		return "Nadaj lub cofnij dostęp innych stref do tego repozytorium."
	case platform.SettingsDialogEditingPolicy:
		return "Wybierz edycję swobodną albo wymagającą wypożyczenia pliku."
	case platform.SettingsDialogPublicShares:
		return "Twórz i zarządzaj publicznymi adresami do pobierania."
	case platform.SettingsDialogUploadChannels:
		return "Zarządzaj półkami, na które odbiorcy mogą przesyłać pliki."
	case platform.SettingsDialogDetachFolder:
		return "Usuń lokalne powiązanie bez kasowania repozytorium na serwerze."
	case platform.SettingsDialogDeleteRepo:
		return "Odłącz i usuń repozytorium zgodnie z polityką retencji."
	case platform.SettingsDialogLoadDump:
		return "Odtwórz zawartość repozytorium z archiwum SVN."
	case platform.SettingsDialogDetachServer:
		return "Odłącz wyłącznie tę instalację; dane strefy pozostaną aktywne."
	case platform.SettingsDialogRemoveRealm:
		return "Usuń repozytoria strefy, cofnij granty i przygotuj odzyskiwanie."
	default:
		return "Wykonaj działanie w aktualnym kontekście FileES."
	}
}
