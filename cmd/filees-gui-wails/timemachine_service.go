package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

const timeMachineContextEvent = "filees:timemachine-context"

// A history read may wait two minutes inside the daemon. The window waits a
// little longer, so the daemon's own answer, not a local deadline, reaches it.
const timeMachineCallTimeout = 2*time.Minute + 15*time.Second

type timeMachineDaemon interface {
	HistoryResolve(context.Context, contract.RepoHistoryResolvePayload) (*contract.RepoHistorySnapshot, error)
	HistoryCommits(context.Context, contract.RepoHistoryCommitsPayload) (*contract.RepoHistoryCommitsResult, error)
	HistoryChanges(context.Context, contract.RepoHistoryChangesPayload) (*contract.RepoHistoryChangesResult, error)
	HistoryList(context.Context, contract.RepoHistoryListPayload) (*contract.RepoHistoryListResult, error)
	HistoryDensity(context.Context, contract.RepoHistoryDensityPayload) (*contract.RepoHistoryDensityResult, error)
	HistoryFetch(context.Context, contract.RepoHistoryFetchPayload) (*contract.RepoHistoryOperation, error)
	HistoryOperation(context.Context, string) (*contract.RepoHistoryOperation, error)
	HistoryConfirm(context.Context, string) (*contract.RepoHistoryOperation, error)
	HistoryCancel(context.Context, string) (*contract.RepoHistoryOperation, error)
}

// TimeMachineService serves the Wehikuł czasu window
// (concepts/REPOSITORY_HISTORY_CONCEPT.md §7). Unlike RepositoryService it
// talks to the daemon itself: the window browses and exports on its own clock,
// long after the gesture that opened it. That is safe because the daemon
// authorises every call again; this service adds only what a page must not be
// trusted with - which repositories are offered, and a destination that is at
// least an absolute path before it travels.
type TimeMachineService struct {
	daemon     timeMachineDaemon
	projection func() Snapshot
	render     func(code, key string, details map[string]string) string
	text       func(key, fallback string) string

	mu      sync.RWMutex
	emitter snapshotEmitter
	show    func()
	hide    func()
	pick    func(context.Context, string, string) (string, error)
	focus   TimeMachineContext
}

// TimeMachineContext is the repository the window was last opened for. The
// sequence changes on every opening, so reopening the same repository still
// brings the page back to it.
type TimeMachineContext struct {
	ServerID string `json:"server_id"`
	RepoID   string `json:"repo_id"`
	Sequence uint64 `json:"sequence"`
}

type TimeMachineRepository struct {
	ServerID   string `json:"server_id"`
	ServerName string `json:"server_name"`
	RepoID     string `json:"repo_id"`
	Name       string `json:"name"`
	LocalPath  string `json:"local_path,omitempty"`
}

type timeMachineBrowserAdapter struct{ service *TimeMachineService }

func newTimeMachineService(daemon timeMachineDaemon, projection func() Snapshot, render func(string, string, map[string]string) string, text func(string, string) string) *TimeMachineService {
	return &TimeMachineService{daemon: daemon, projection: projection, render: render, text: text}
}

func (service *TimeMachineService) attachEmitter(emitter snapshotEmitter) {
	service.mu.Lock()
	service.emitter = emitter
	service.mu.Unlock()
}

func (service *TimeMachineService) attachPresentation(show, hide func()) {
	service.mu.Lock()
	service.show, service.hide = show, hide
	service.mu.Unlock()
}

func (service *TimeMachineService) attachPicker(pick func(context.Context, string, string) (string, error)) {
	service.mu.Lock()
	service.pick = pick
	service.mu.Unlock()
}

// wailsHistoryPicker adapts the native folder dialog; a cancelled dialog is an
// empty path, not an error.
func wailsHistoryPicker(picker wailsFolderPicker) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, title, initialDir string) (string, error) {
		result, err := picker.PickFolder(ctx, platform.PickFolderRequest{Title: title, InitialDir: initialDir})
		if err != nil || result.Cancelled {
			return "", err
		}
		return result.Path, nil
	}
}

func timeMachineOffered(snapshot Snapshot) bool {
	for _, capability := range snapshot.Capabilities {
		if capability == contract.CapRepoHistory {
			return true
		}
	}
	return false
}

// Repositories lists what the window may offer: ordinary repositories the
// projection shows as owned, on a daemon that advertises history. Guests,
// shelves and deleted repositories are not offered; the daemon would refuse
// them anyway, and an entry that can only fail is not an entry.
func (service *TimeMachineService) Repositories() []TimeMachineRepository {
	snapshot := service.projection()
	out := []TimeMachineRepository{}
	if !snapshot.Connected || snapshot.Stale || !timeMachineOffered(snapshot) {
		return out
	}
	servers := make(map[string]string, len(snapshot.Servers))
	for _, server := range snapshot.Servers {
		servers[server.ID] = firstNonBlank(server.DisplayName, server.ID)
	}
	for _, repo := range snapshot.Repositories {
		if repo.Ownership != "owned" || repo.Purpose != "" || repo.ServerDeleted {
			continue
		}
		out = append(out, TimeMachineRepository{
			ServerID: repo.ServerID, ServerName: firstNonBlank(servers[repo.ServerID], repo.ServerID),
			RepoID: repo.ID, Name: firstNonBlank(repo.DisplayName, repo.ID), LocalPath: repo.LocalPath,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServerName != out[j].ServerName {
			return out[i].ServerName < out[j].ServerName
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (service *TimeMachineService) offers(serverID, repoID string) bool {
	for _, repo := range service.Repositories() {
		if repo.ServerID == serverID && repo.RepoID == repoID {
			return true
		}
	}
	return false
}

// Context is the repository the window was last opened for.
func (service *TimeMachineService) Context() TimeMachineContext {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.focus
}

func (service *TimeMachineService) open(serverID, repoID string) error {
	if repoID != "" && !service.offers(serverID, repoID) {
		return errors.New("repository is not offered by the time machine")
	}
	service.mu.Lock()
	service.focus = TimeMachineContext{ServerID: serverID, RepoID: repoID, Sequence: service.focus.Sequence + 1}
	focus, emitter, show := service.focus, service.emitter, service.show
	service.mu.Unlock()
	if emitter != nil {
		emitter.Emit(timeMachineContextEvent, focus)
	}
	if show != nil {
		show()
	}
	return nil
}

func (adapter timeMachineBrowserAdapter) OpenHistory(_ context.Context, request platform.HistoryOpenRequest) error {
	return adapter.service.open(request.ServerID, request.RepoID)
}

// Close hides the window. It does not cancel anything: a confirmed export
// belongs to the daemon.
func (service *TimeMachineService) Close() {
	service.mu.RLock()
	hide := service.hide
	service.mu.RUnlock()
	if hide != nil {
		hide()
	}
}

// failure turns a daemon refusal into the sentence the domain catalogue holds
// for its key. The page shows the sentence; it never parses it.
func (service *TimeMachineService) failure(err error) error {
	var refusal *ipcclient.ResponseError
	if errors.As(err, &refusal) {
		code, _, _, key := refusal.PresentationError()
		return errors.New(service.render(code, key, nil))
	}
	return errors.New(service.render("HISTORY-1001", "history.read_failed", nil))
}

func timeMachineCall[T any](service *TimeMachineService, call func(context.Context) (*T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(context.Background(), timeMachineCallTimeout)
	defer cancel()
	result, err := call(ctx)
	if err != nil {
		return zero, service.failure(err)
	}
	if result == nil {
		return zero, service.failure(errors.New("empty daemon answer"))
	}
	return *result, nil
}

func (service *TimeMachineService) Resolve(serverID, repoID, momentUTC, boundary string) (contract.RepoHistorySnapshot, error) {
	if !service.offers(serverID, repoID) {
		return contract.RepoHistorySnapshot{}, errors.New(service.render("HISTORY-2001", "history.forbidden", nil))
	}
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistorySnapshot, error) {
		return service.daemon.HistoryResolve(ctx, contract.RepoHistoryResolvePayload{ServerID: serverID, RepoID: repoID, MomentUTC: momentUTC, Boundary: boundary})
	})
}

func (service *TimeMachineService) Commits(snapshotID, from, to, cursor string) (contract.RepoHistoryCommitsResult, error) {
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryCommitsResult, error) {
		return service.daemon.HistoryCommits(ctx, contract.RepoHistoryCommitsPayload{SnapshotID: snapshotID, From: from, To: to, Cursor: cursor})
	})
}

func (service *TimeMachineService) Changes(snapshotID string, revision int64, cursor string) (contract.RepoHistoryChangesResult, error) {
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryChangesResult, error) {
		return service.daemon.HistoryChanges(ctx, contract.RepoHistoryChangesPayload{SnapshotID: snapshotID, Revision: revision, Cursor: cursor})
	})
}

func (service *TimeMachineService) List(snapshotID, path, cursor string) (contract.RepoHistoryListResult, error) {
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryListResult, error) {
		return service.daemon.HistoryList(ctx, contract.RepoHistoryListPayload{SnapshotID: snapshotID, Path: path, Cursor: cursor})
	})
}

func (service *TimeMachineService) Density(snapshotID string, bucketHours, utcOffsetMinutes int, cursor string) (contract.RepoHistoryDensityResult, error) {
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryDensityResult, error) {
		return service.daemon.HistoryDensity(ctx, contract.RepoHistoryDensityPayload{SnapshotID: snapshotID, BucketHours: bucketHours, UTCOffsetMinutes: utcOffsetMinutes, Cursor: cursor})
	})
}

// ChooseDestination asks for the parent folder of a copy. An empty answer is
// a cancelled dialog. The daemon still refuses a folder inside a working copy.
func (service *TimeMachineService) ChooseDestination() (string, error) {
	service.mu.RLock()
	pick := service.pick
	service.mu.RUnlock()
	if pick == nil {
		return "", errors.New(service.text("timeMachine.pickerUnavailable", "Wybór folderu jest niedostępny"))
	}
	home, _ := os.UserHomeDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return pick(ctx, service.text("timeMachine.chooseDestination", "Wybierz folder, w którym powstanie kopia"), home)
}

func (service *TimeMachineService) Fetch(payload contract.RepoHistoryFetchPayload) (contract.RepoHistoryOperation, error) {
	if !filepath.IsAbs(payload.DestinationParent) {
		return contract.RepoHistoryOperation{}, errors.New(service.render("HISTORY-2005", "history.destination_refused", nil))
	}
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryOperation, error) {
		return service.daemon.HistoryFetch(ctx, payload)
	})
}

func (service *TimeMachineService) Operation(operationID string) (contract.RepoHistoryOperation, error) {
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryOperation, error) {
		return service.daemon.HistoryOperation(ctx, operationID)
	})
}

func (service *TimeMachineService) Confirm(operationID string) (contract.RepoHistoryOperation, error) {
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryOperation, error) {
		return service.daemon.HistoryConfirm(ctx, operationID)
	})
}

func (service *TimeMachineService) Cancel(operationID string) (contract.RepoHistoryOperation, error) {
	return timeMachineCall(service, func(ctx context.Context) (*contract.RepoHistoryOperation, error) {
		return service.daemon.HistoryCancel(ctx, operationID)
	})
}
