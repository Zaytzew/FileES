package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

const headBrowserContextEvent = "filees:head-browser-context"

// Listing and reading wait up to minutes inside the daemon; choosing a path
// can check out a large subtree. The window waits past the daemon's own bound.
const (
	headBrowserReadTimeout  = 11 * time.Minute
	headBrowserWriteTimeout = 31 * time.Minute
)

type headBrowserDaemon interface {
	HeadList(context.Context, contract.RepoHeadListPayload) (*contract.RepoHeadListResult, error)
	HeadCat(context.Context, contract.RepoHeadCatPayload) (*contract.RepoHeadCatResult, error)
	HeadMaterialize(context.Context, contract.RepoHeadMaterializePayload) (*contract.RepoHeadWriteResult, error)
	AnchorCreate(context.Context, contract.RepoAnchorCreatePayload) (*contract.RepoHeadWriteResult, error)
	HeadFill(context.Context, contract.RepoHeadFillPayload) (*contract.RepoHeadWriteResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

// HeadBrowserService serves the "Browse on the server" window
// (implementation notes (not distributed)): the tree of a repository with
// no copy on this computer, or with only chosen paths. Like the time machine
// it talks to the daemon itself and the daemon authorises every call again;
// this service decides only which repository the window may be focused on
// and where the first local copy goes.
type HeadBrowserService struct {
	daemon     headBrowserDaemon
	projection func() Snapshot
	render     func(code, key string, details map[string]string) string
	text       func(key, fallback string) string

	mu      sync.RWMutex
	emitter snapshotEmitter
	show    func()
	hide    func()
	pick    func(context.Context, string, string) (string, error)
	open    func(context.Context, string) error
	focus   HeadBrowserContext
}

// HeadBrowserContext is the repository the window was last opened for.
type HeadBrowserContext struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Sequence uint64 `json:"sequence"`
}

// HeadBrowserRepository is what the page needs to know about the focused
// repository, read from the projection each time.
type HeadBrowserRepository struct {
	ServerID   string `json:"server_id"`
	ServerName string `json:"server_name"`
	RepoID     string `json:"repo_id"`
	Name       string `json:"name"`
	Access     string `json:"access"`
	Attached   bool   `json:"attached"`
	Sparse     bool   `json:"sparse"`
	LocalPath  string `json:"local_path,omitempty"`
}

func newHeadBrowserService(daemon headBrowserDaemon, projection func() Snapshot, render func(string, string, map[string]string) string, text func(string, string) string) *HeadBrowserService {
	return &HeadBrowserService{daemon: daemon, projection: projection, render: render, text: text}
}

func (service *HeadBrowserService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

func (service *HeadBrowserService) attachPresentation(show, hide func()) {
	service.mu.Lock()
	service.show, service.hide = show, hide
	service.mu.Unlock()
}

func (service *HeadBrowserService) attachPlatform(pick func(context.Context, string, string) (string, error), open func(context.Context, string) error) {
	service.mu.Lock()
	service.pick, service.open = pick, open
	service.mu.Unlock()
}

// lookup finds a browsable repository in the current projection: ordinary,
// readable, not deleted. The daemon checks the same again on every call.
func (service *HeadBrowserService) lookup(serverID, repoID string) (HeadBrowserRepository, bool) {
	snapshot := service.projection()
	if !snapshot.Connected || snapshot.Stale {
		return HeadBrowserRepository{}, false
	}
	offered := false
	for _, capability := range snapshot.Capabilities {
		offered = offered || capability == contract.CapRepoHeadBrowse
	}
	if !offered {
		return HeadBrowserRepository{}, false
	}
	for _, repo := range snapshot.Repositories {
		if repo.ServerID != serverID || repo.ID != repoID {
			continue
		}
		if repo.Purpose != "" || repo.ServerDeleted || (repo.Access != "r" && repo.Access != "rw") {
			return HeadBrowserRepository{}, false
		}
		serverName := serverID
		for _, server := range snapshot.Servers {
			if server.ID == serverID {
				serverName = firstNonBlank(server.DisplayName, server.ID)
			}
		}
		return HeadBrowserRepository{
			ServerID: serverID, ServerName: serverName, RepoID: repoID,
			Name: firstNonBlank(repo.DisplayName, repo.ID), Access: repo.Access,
			Attached: repo.Attached, Sparse: repo.Sparse, LocalPath: repo.LocalPath,
		}, true
	}
	return HeadBrowserRepository{}, false
}

func (service *HeadBrowserService) focused() (HeadBrowserRepository, error) {
	service.mu.RLock()
	focus := service.focus
	service.mu.RUnlock()
	repo, ok := service.lookup(focus.ServerID, focus.RepoID)
	if !ok {
		return HeadBrowserRepository{}, errors.New(service.render("HEAD-2001", "head.forbidden", nil))
	}
	return repo, nil
}

// Open focuses the window on one repository and shows it. The main window
// calls it from the repository row.
func (service *HeadBrowserService) Open(serverID, repoID string) error {
	if _, ok := service.lookup(serverID, repoID); !ok {
		return errors.New(service.render("HEAD-2001", "head.forbidden", nil))
	}
	service.mu.Lock()
	service.focus = HeadBrowserContext{ServerID: serverID, RepoID: repoID, Sequence: service.focus.Sequence + 1}
	focus, emitter, show := service.focus, service.emitter, service.show
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(headBrowserContextEvent, focus)
	}
	if show != nil {
		show()
	}
	return nil
}

// Context is the repository the window was last opened for.
func (service *HeadBrowserService) Context() HeadBrowserContext {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.focus
}

// Repository describes the focused repository as the projection shows it now:
// the page re-reads it after a choice to learn that a copy appeared.
func (service *HeadBrowserService) Repository() (HeadBrowserRepository, error) {
	return service.focused()
}

func (service *HeadBrowserService) failure(err error, code, key string) error {
	var refusal *ipcclient.ResponseError
	if errors.As(err, &refusal) {
		refusalCode, _, _, refusalKey := refusal.PresentationError()
		return errors.New(service.render(refusalCode, refusalKey, nil))
	}
	return errors.New(service.render(code, key, nil))
}

func headBrowserCall[T any](service *HeadBrowserService, timeout time.Duration, code, key string, call func(context.Context) (*T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := call(ctx)
	if err != nil {
		return zero, service.failure(err, code, key)
	}
	if result == nil {
		return zero, service.failure(errors.New("empty daemon answer"), code, key)
	}
	return *result, nil
}

// List reads one folder at HEAD. Entries already on this computer carry local.
func (service *HeadBrowserService) List(path string) (contract.RepoHeadListResult, error) {
	repo, err := service.focused()
	if err != nil {
		return contract.RepoHeadListResult{}, err
	}
	return headBrowserCall(service, headBrowserReadTimeout, "HEAD-2003", "head.list_failed", func(ctx context.Context) (*contract.RepoHeadListResult, error) {
		return service.daemon.HeadList(ctx, contract.RepoHeadListPayload{ServerID: repo.ServerID, RepoID: repo.RepoID, Path: path})
	})
}

// Preview reads one file at HEAD into a temporary file and opens it with the
// system's application for its type. Nothing is written back: to edit, bring
// the path to this computer first (§1).
func (service *HeadBrowserService) Preview(path string) error {
	repo, err := service.focused()
	if err != nil {
		return err
	}
	result, err := headBrowserCall(service, headBrowserReadTimeout, "HEAD-2004", "head.cat_failed", func(ctx context.Context) (*contract.RepoHeadCatResult, error) {
		return service.daemon.HeadCat(ctx, contract.RepoHeadCatPayload{ServerID: repo.ServerID, RepoID: repo.RepoID, Path: path})
	})
	if err != nil {
		return err
	}
	return service.openPath(result.File)
}

// OpenLocal opens a path of the sparse copy that is already on this computer.
func (service *HeadBrowserService) OpenLocal(path string) error {
	repo, err := service.focused()
	if err != nil {
		return err
	}
	if !repo.Attached || !filepath.IsAbs(repo.LocalPath) {
		return errors.New(service.render("HEAD-2005", "head.anchor_required", nil))
	}
	clean := filepath.Clean(filepath.FromSlash(strings.Trim(path, "/")))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return errors.New(service.render("HEAD-2002", "head.bad_path", nil))
	}
	return service.openPath(filepath.Join(repo.LocalPath, clean))
}

func (service *HeadBrowserService) openPath(path string) error {
	service.mu.RLock()
	open := service.open
	service.mu.RUnlock()
	if open == nil {
		return errors.New(service.text("headBrowser.openUnavailable", "Nie można otworzyć pliku na tym komputerze"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return open(ctx, path)
}

// Materialize brings one path onto this computer. The first path of a
// repository without a copy asks where the copy goes - the folder picker is
// shown here and only here; every later path joins that same copy.
func (service *HeadBrowserService) Materialize(path string) (contract.RepoHeadWriteResult, error) {
	repo, err := service.focused()
	if err != nil {
		return contract.RepoHeadWriteResult{}, err
	}
	payload := contract.RepoHeadMaterializePayload{ServerID: repo.ServerID, RepoID: repo.RepoID, Path: path}
	if !repo.Attached {
		anchor, err := service.chooseAnchor(repo)
		if err != nil || anchor == "" {
			return contract.RepoHeadWriteResult{State: "cancelled"}, err
		}
		// Where partial attachments are Explorer anchors (Windows builds with
		// the Cloud Files API), the first path makes the chosen folder an
		// anchor; its placeholders then show the whole tree (owner,
		// 2026-09-28: one mode per build).
		if service.partialAnchor() {
			return headBrowserCall(service, headBrowserWriteTimeout, "HEAD-2006", "head.materialize_failed", func(ctx context.Context) (*contract.RepoHeadWriteResult, error) {
				return service.daemon.AnchorCreate(ctx, contract.RepoAnchorCreatePayload{ServerID: repo.ServerID, RepoID: repo.RepoID, LocalPath: anchor})
			})
		}
		payload.LocalPath = anchor
	}
	return headBrowserCall(service, headBrowserWriteTimeout, "HEAD-2006", "head.materialize_failed", func(ctx context.Context) (*contract.RepoHeadWriteResult, error) {
		return service.daemon.HeadMaterialize(ctx, payload)
	})
}

// partialAnchor reports whether the daemon attaches partially as Explorer
// anchors only (capability repo.partial_anchor).
func (service *HeadBrowserService) partialAnchor() bool {
	if service.projection == nil {
		return false
	}
	for _, capability := range service.projection().Capabilities {
		if capability == contract.CapRepoPartialAnchor {
			return true
		}
	}
	return false
}

// chooseAnchor asks for the parent folder and names the copy after the
// repository, the way a first Connect would, so the choice is one familiar
// dialog rather than a folder that must already be empty.
func (service *HeadBrowserService) chooseAnchor(repo HeadBrowserRepository) (string, error) {
	service.mu.RLock()
	pick := service.pick
	service.mu.RUnlock()
	if pick == nil {
		return "", errors.New(service.text("headBrowser.pickerUnavailable", "Wybór folderu jest niedostępny"))
	}
	home, _ := os.UserHomeDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	parent, err := pick(ctx, service.text("headBrowser.chooseAnchor", "Wybierz folder, w którym powstanie kopia"), home)
	if err != nil || strings.TrimSpace(parent) == "" {
		return "", err
	}
	return filepath.Join(parent, safeFolderName(repo.Name, repo.RepoID)), nil
}

// safeFolderName keeps a repository name usable as one folder on every
// desktop: no separators, no reserved characters, no trailing dots or spaces.
func safeFolderName(name, fallback string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	cleaned = strings.TrimRight(cleaned, ". ")
	if cleaned == "" || cleaned == "_" {
		return fallback
	}
	return cleaned
}

// Fill brings the rest of the tree into the same copy, with no picker (§1a).
// A repository without a copy has nothing to fill; its row offers Connect.
func (service *HeadBrowserService) Fill() (contract.RepoHeadWriteResult, error) {
	repo, err := service.focused()
	if err != nil {
		return contract.RepoHeadWriteResult{}, err
	}
	if !repo.Attached {
		return contract.RepoHeadWriteResult{}, errors.New(service.render("HEAD-2005", "head.anchor_required", nil))
	}
	return headBrowserCall(service, headBrowserWriteTimeout, "HEAD-2007", "head.fill_failed", func(ctx context.Context) (*contract.RepoHeadWriteResult, error) {
		return service.daemon.HeadFill(ctx, contract.RepoHeadFillPayload{ServerID: repo.ServerID, RepoID: repo.RepoID})
	})
}

// Operation reports the sparse attachment the first chosen path started.
func (service *HeadBrowserService) Operation(operationID string) (contract.RepoLifecycleResult, error) {
	return headBrowserCall(service, time.Minute, "HEAD-2006", "head.materialize_failed", func(ctx context.Context) (*contract.RepoLifecycleResult, error) {
		return service.daemon.RepoLifecycleStatus(ctx, operationID)
	})
}

// Close hides the window. A path already being brought in keeps going: it
// belongs to the daemon.
func (service *HeadBrowserService) Close() {
	service.mu.RLock()
	hide := service.hide
	service.mu.RUnlock()
	if hide != nil {
		hide()
	}
}
