package platform

// Settings sequence is server → action → working-copy list, never the reverse.
// Folder-scoped options must not require picking a share first: realm
// visibility, session wait and deactivate have no working copy.

type settingsActionSpec struct {
	Action      SettingsDialogAction
	ID          string
	WindowsID   string
	Label       string
	NeedsFolder bool
	Multi       bool
}

// Order is the user-facing list. Local and realm actions come first.
// Destructive server actions stay last so they are not the default row.
var settingsActionCatalog = []settingsActionSpec{
	{SettingsDialogSessionTimeout, "session_timeout", "session_timeout", "Limit czasu wysyłki i pobierania…", false, false},
	{SettingsDialogRealmVisibility, "realm_visibility", "realm_visibility", "Widoczność mojej strefy", false, false},
	{SettingsDialogRealmBranding, "realm_branding", "realm_branding", "Wygląd udziałów publicznych", false, false},
	{SettingsDialogRealmAlias, "realm_alias", "realm_alias", "Ustaw stały alias", false, false},
	{SettingsDialogPairMobile, "pair_mobile", "pair_mobile", "Sparuj urządzenie mobilne", false, false},
	{SettingsDialogAddFolder, "add_folder", "add", "Dodaj folder do FileES", false, false},
	{SettingsDialogConnectRepos, "connect_repositories", "connect", "Połącz z istniejącymi udziałami strefy", true, true},
	{SettingsDialogLocateFolder, "locate_folder", "locate", "Wskaż przeniesioną kopię roboczą", true, false},
	{SettingsDialogRetryLifecycle, "retry_lifecycle", "retry_lifecycle", "Ponów niedokończone działanie", true, false},
	{SettingsDialogAbandonLifecycle, "abandon_lifecycle", "abandon_lifecycle", "Zakończ starą próbę lokalną", true, false},
	{SettingsDialogManageGrants, "manage_grants", "manage_grants", "Uprawnienia gości", true, false},
	{SettingsDialogEditingPolicy, "editing_policy", "editing_policy", "Zasady edycji", true, false},
	{SettingsDialogPublicShares, "public_shares", "public_shares", "Udostępnienia publiczne", true, false},
	{SettingsDialogUploadChannels, "upload_channels", "upload_channels", "Półki przyjęcia", true, false},
	{SettingsDialogDetachFolder, "detach_folder", "detach", "Odłącz tylko folder", true, false},
	{SettingsDialogDeleteRepo, "delete_repository", "delete", "Usuń repozytorium", true, false},
	{SettingsDialogLoadDump, "load_dump", "load_dump", "Odtwórz z archiwum", true, false},
	{SettingsDialogDetachServer, "detach_server", "deactivate", "Dezaktywuj tylko tego klienta", false, false},
	{SettingsDialogRemoveRealm, "remove_realm", "remove_realm", "Usuń mój udział FileES z serwera", false, false},
}

type SettingsWizard struct {
	Title, Text string
	Servers     []SettingsWizardServer
	Recoveries  []SettingsRecovery
}

type SettingsWizardServer struct {
	ID, Name, Address, Realm, ClientID string
	Actions                            []SettingsWizardAction
}

type SettingsWizardAction struct {
	ID, WindowsID, Label string
	Action               SettingsDialogAction `json:"-"`
	NeedsFolder, Multi   bool
	Folders              []SettingsWizardFolder
}

type SettingsWizardFolder struct {
	ID, Name, Path, State, Access, Editing string
}

func BuildSettingsWizard(request SettingsDialogRequest) SettingsWizard {
	wizard := SettingsWizard{Title: request.Title, Text: request.Text, Recoveries: append([]SettingsRecovery(nil), request.Recoveries...)}
	for _, server := range request.Servers {
		item := SettingsWizardServer{ID: server.ID, Name: server.Name, Address: server.Address, Realm: server.Realm, ClientID: server.ClientID}
		for _, spec := range settingsActionCatalog {
			action, ok := wizardActionForServer(server, spec)
			if !ok {
				continue
			}
			item.Actions = append(item.Actions, action)
		}
		wizard.Servers = append(wizard.Servers, item)
	}
	return wizard
}

func wizardActionForServer(server SettingsServer, spec settingsActionSpec) (SettingsWizardAction, bool) {
	if !serverAllowsSettingsAction(server, spec) {
		return SettingsWizardAction{}, false
	}
	action := SettingsWizardAction{ID: spec.ID, WindowsID: spec.WindowsID, Label: spec.Label, Action: spec.Action, NeedsFolder: spec.NeedsFolder, Multi: spec.Multi}
	if !spec.NeedsFolder {
		return action, true
	}
	for _, folder := range server.Folders {
		if !folderAllowsSettingsAction(folder, spec.Action) {
			continue
		}
		action.Folders = append(action.Folders, SettingsWizardFolder{
			ID: folder.ID, Name: folder.Name, Path: folder.LocalPath,
			State: folder.State, Access: folder.Access, Editing: folder.Editing,
		})
	}
	if len(action.Folders) == 0 {
		return SettingsWizardAction{}, false
	}
	return action, true
}

func serverAllowsSettingsAction(server SettingsServer, spec settingsActionSpec) bool {
	switch spec.Action {
	case SettingsDialogSessionTimeout:
		return server.CanSetSessionTimeout
	case SettingsDialogRealmVisibility:
		return server.CanSetRealmVisibility
	case SettingsDialogRealmBranding:
		return server.CanSetRealmBranding
	case SettingsDialogRealmAlias:
		return server.CanClaimRealmAlias
	case SettingsDialogPairMobile:
		return server.CanPairMobile
	case SettingsDialogAddFolder:
		return server.CanAddFolder
	case SettingsDialogDetachServer, SettingsDialogRemoveRealm:
		return true
	default:
		for _, folder := range server.Folders {
			if folderAllowsSettingsAction(folder, spec.Action) {
				return true
			}
		}
		return false
	}
}

func folderAllowsSettingsAction(folder SettingsFolder, action SettingsDialogAction) bool {
	switch action {
	case SettingsDialogConnectRepos:
		return folder.CanConnect
	case SettingsDialogLocateFolder:
		return folder.CanLocate
	case SettingsDialogManageGrants:
		return folder.CanManageGrants
	case SettingsDialogEditingPolicy:
		return folder.CanSetEditingPolicy
	case SettingsDialogPublicShares:
		return folder.CanManagePublicShares
	case SettingsDialogUploadChannels:
		return folder.CanManageUploadChannels
	case SettingsDialogQuarantine:
		return folder.CanReviewQuarantine
	case SettingsDialogDetachFolder:
		return folder.CanDetach
	case SettingsDialogDeleteRepo:
		return folder.CanDelete
	case SettingsDialogLoadDump:
		return folder.CanLoadDump
	case SettingsDialogRetryLifecycle:
		return folder.CanRetryLifecycle
	case SettingsDialogAbandonLifecycle:
		return folder.CanAbandonLifecycle
	default:
		return false
	}
}

func settingsActionFromID(id string) SettingsDialogAction {
	switch id {
	case "add_folder", "add", "Dodaj folder":
		return SettingsDialogAddFolder
	case "connect_repositories", "connect", "Połącz":
		return SettingsDialogConnectRepos
	case "locate_folder", "locate":
		return SettingsDialogLocateFolder
	case "detach_folder", "detach", "Odłącz folder":
		return SettingsDialogDetachFolder
	case "delete_repository", "delete", "Odłącz trwale":
		return SettingsDialogDeleteRepo
	case "load_dump", "Odtwórz z archiwum":
		return SettingsDialogLoadDump
	case "retry_lifecycle":
		return SettingsDialogRetryLifecycle
	case "abandon_lifecycle":
		return SettingsDialogAbandonLifecycle
	case "manage_grants":
		return SettingsDialogManageGrants
	case "editing_policy":
		return SettingsDialogEditingPolicy
	case "public_shares":
		return SettingsDialogPublicShares
	case "upload_channels":
		return SettingsDialogUploadChannels
	case "realm_visibility":
		return SettingsDialogRealmVisibility
	case "realm_branding":
		return SettingsDialogRealmBranding
	case "realm_alias":
		return SettingsDialogRealmAlias
	case "pair_mobile":
		return SettingsDialogPairMobile
	case "session_timeout":
		return SettingsDialogSessionTimeout
	case "detach_server", "deactivate", "Dezaktywuj klienta":
		return SettingsDialogDetachServer
	case "remove_realm":
		return SettingsDialogRemoveRealm
	default:
		return SettingsDialogClose
	}
}

func findWizardServer(wizard SettingsWizard, serverID string) (SettingsWizardServer, bool) {
	for _, server := range wizard.Servers {
		if server.ID == serverID {
			return server, true
		}
	}
	return SettingsWizardServer{}, false
}

func findWizardAction(server SettingsWizardServer, id string) (SettingsWizardAction, bool) {
	for _, action := range server.Actions {
		if action.ID == id || action.WindowsID == id {
			return action, true
		}
	}
	return SettingsWizardAction{}, false
}
