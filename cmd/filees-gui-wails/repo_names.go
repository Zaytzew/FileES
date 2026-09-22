package main

import (
	"errors"
	"strings"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/reponames"
)

// A repository's shown name is this client's business alone. The name set
// here lives in the GUI's own file and is laid over the daemon's model where
// that model enters the GUI (onChange), so the window, the tray, notifications
// and dialogs all show the same name. Nothing is sent to the daemon, nothing
// on disk is renamed, and the server keeps the name the repository has.

// RepositoryNaming is what the rename field needs: the name shown now and the
// repository's own name, to bring back.
type RepositoryNaming struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Name     string `json:"name"`
	OwnName  string `json:"own_name"`
	Custom   bool   `json:"custom"`
}

var errRepositoryNotShown = errors.New("repository is not in the current view")

func (service *GUIService) attachRepoNames(store *reponames.Store, renamed func(serverID, repoID, name string)) {
	service.mu.Lock()
	service.repoNames = store
	service.onRepoRenamed = renamed
	service.mu.Unlock()
}

func (service *GUIService) applyRepoNames(vm guiapp.ViewModel) guiapp.ViewModel {
	service.mu.RLock()
	store := service.repoNames
	service.mu.RUnlock()
	if store == nil {
		return vm
	}
	names := make(map[string]string)
	repos := make([]guiapp.RepoViewModel, len(vm.Repos))
	copy(repos, vm.Repos)
	for i := range repos {
		if name, ok := store.Name(repos[i].ServerID, repos[i].ID); ok {
			repos[i].DisplayName = name
			names[repos[i].ServerID+"\x00"+repos[i].ID] = name
		}
	}
	vm.Repos = repos
	if len(names) > 0 && len(vm.PublicShares) > 0 {
		shares := make([]guiapp.PublicShareViewModel, len(vm.PublicShares))
		copy(shares, vm.PublicShares)
		for i := range shares {
			if name, ok := names[shares[i].ServerID+"\x00"+shares[i].RepoID]; ok {
				shares[i].RepoDisplayName = name
			}
		}
		vm.PublicShares = shares
	}
	return vm
}

// markOwnNames tells the window which rows show a name set here, so it can
// say what the repository is really called.
func markOwnNames(snapshot *Snapshot, daemonView guiapp.ViewModel) {
	own := make(map[string]string, len(daemonView.Repos))
	for _, repo := range daemonView.Repos {
		own[repo.ServerID+"\x00"+repo.ID] = firstNonBlank(repo.DisplayName, repo.ID)
	}
	for i := range snapshot.Repositories {
		repo := &snapshot.Repositories[i]
		if name, ok := own[repo.ServerID+"\x00"+repo.ID]; ok && name != firstNonBlank(repo.DisplayName, repo.ID) {
			repo.OwnName = name
		}
	}
}

func (service *GUIService) repositoryNaming(serverID, repoID string) (RepositoryNaming, error) {
	service.mu.RLock()
	daemonView, store := service.daemonView, service.repoNames
	service.mu.RUnlock()
	for _, repo := range daemonView.Repos {
		if repo.ServerID != serverID || repo.ID != repoID {
			continue
		}
		naming := RepositoryNaming{ServerID: serverID, RepoID: repoID, OwnName: firstNonBlank(repo.DisplayName, repo.ID)}
		naming.Name = naming.OwnName
		if name, ok := store.Name(serverID, repoID); ok {
			naming.Name, naming.Custom = name, true
		}
		return naming, nil
	}
	return RepositoryNaming{}, errRepositoryNotShown
}

// RepositoryNaming returns the shown and the own name of one repository.
func (service *GUIService) RepositoryNaming(serverID, repoID string) (RepositoryNaming, error) {
	return service.repositoryNaming(strings.TrimSpace(serverID), strings.TrimSpace(repoID))
}

// RenameRepository sets the name this computer shows for a repository. An
// empty name, or the repository's own name, brings the own name back.
func (service *GUIService) RenameRepository(serverID, repoID, name string) (RepositoryNaming, error) {
	serverID, repoID = strings.TrimSpace(serverID), strings.TrimSpace(repoID)
	naming, err := service.repositoryNaming(serverID, repoID)
	if err != nil {
		return RepositoryNaming{}, err
	}
	name, ok := reponames.Normalize(name)
	if !ok {
		return RepositoryNaming{}, reponames.ErrInvalidName
	}
	if name == naming.OwnName {
		name = ""
	}
	service.mu.RLock()
	store, renamed, daemonView := service.repoNames, service.onRepoRenamed, service.daemonView
	service.mu.RUnlock()
	if err := store.Set(serverID, repoID, name); err != nil {
		return RepositoryNaming{}, err
	}
	service.onChange(daemonView)
	naming, err = service.repositoryNaming(serverID, repoID)
	if err == nil && renamed != nil {
		renamed(serverID, repoID, naming.Name)
	}
	return naming, err
}
