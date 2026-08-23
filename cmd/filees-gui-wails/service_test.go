package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
)

func TestProjectViewModelKeepsRendererOnPresentationBoundary(t *testing.T) {
	operation := "commit"
	refreshed := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	vm := guiapp.ViewModel{
		Connected: true, DaemonState: "running", UptimeSec: 42,
		LastRefresh: refreshed, Icon: guiapp.IconBusy,
		Capabilities: map[string]bool{"repo.status": true, "ignored": false, "repo.list": true},
		Repos: []guiapp.RepoViewModel{{
			ID: "repo-1", ServerID: "server-1", DisplayName: "Projekt",
			LocalPath: `E:\Projekt`, Attached: true, Access: contract.AccessReadWrite,
			State: contract.StateActive, Connectivity: contract.ConnOnline,
			LocalRev: 7, HeadRev: 8, CurrentOp: &operation,
			Pending: contract.PendingStats{Added: 1, Modified: 2, Deleted: 3, TotalBytes: 4096},
		}},
		Servers: []guiapp.ServerViewModel{{ID: "server-1", DisplayName: "Spot"}},
	}

	got := projectViewModel(vm)
	if !got.Connected || got.IconState != "busy" || got.LastRefresh != "2026-08-23T10:00:00Z" {
		t.Fatalf("unexpected top-level projection: %+v", got)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"repo.list", "repo.status"}) {
		t.Fatalf("capabilities = %#v", got.Capabilities)
	}
	if len(got.Repositories) != 1 {
		t.Fatalf("repositories = %#v", got.Repositories)
	}
	repo := got.Repositories[0]
	if repo.PendingFiles != 6 || repo.PendingBytes != 4096 || repo.CurrentOperation != "commit" || repo.DisplayState != "busy" {
		t.Fatalf("repository projection = %+v", repo)
	}
}

func TestProjectViewModelClassifiesOwnershipWithoutExposingRealmIDs(t *testing.T) {
	vm := guiapp.ViewModel{
		Servers: []guiapp.ServerViewModel{{ID: "server", RealmID: "realm-local"}},
		Repos: []guiapp.RepoViewModel{
			{ID: "own", ServerID: "server", OwnerRealmID: "realm-local"},
			{ID: "guest", ServerID: "server", OwnerRealmID: "realm-foreign"},
			{ID: "unknown", ServerID: "server"},
		},
	}
	got := projectViewModel(vm)
	if got.Repositories[0].Ownership != "owned" || got.Repositories[1].Ownership != "guest" || got.Repositories[2].Ownership != "unclassified" {
		t.Fatalf("ownership projection = %#v", got.Repositories)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "realm-local") || strings.Contains(string(encoded), "realm-foreign") {
		t.Fatalf("raw realm IDs leaked into renderer JSON: %s", encoded)
	}
}

func TestProjectViewModelBuildsSharedJournalWithTranslatedAndExactTime(t *testing.T) {
	now := time.Date(2026, 8, 23, 14, 0, 0, 0, time.Local)
	vm := guiapp.ViewModel{
		Repos: []guiapp.RepoViewModel{{ID: "docs", DisplayName: "Dokumenty"}},
		Activity: []guiapp.ActivityViewModel{{
			RepoID: "docs", Path: "plan.dwg", Kind: "modified", Stage: "published",
			Revision: 8, UpdatedAt: now.Add(-4 * time.Minute).Format(time.RFC3339),
		}},
	}
	got := projectViewModelAt(vm, now)
	if len(got.Journal) != 1 {
		t.Fatalf("journal=%#v", got.Journal)
	}
	entry := got.Journal[0]
	if entry.RelativeTime != "4 minuty temu" || entry.ExactTime == "" || entry.Repository != "Dokumenty" || !strings.Contains(entry.Summary, "plan.dwg") {
		t.Fatalf("journal entry=%#v", entry)
	}
}

func TestProjectViewModelCarriesDaemonCycleAndPendingAction(t *testing.T) {
	started := time.Date(2026, 8, 23, 14, 0, 0, 0, time.UTC)
	earlier := started.Add(20 * time.Second).Format(time.RFC3339Nano)
	later := started.Add(40 * time.Second).Format(time.RFC3339Nano)
	vm := guiapp.ViewModel{
		Repos: []guiapp.RepoViewModel{
			{ID: "docs", Cycle: contract.CycleStatus{ID: 7, Phase: contract.CycleWaiting, NextTickAt: later}},
			{ID: "drawings", Cycle: contract.CycleStatus{ID: 9, Phase: contract.CycleRunning}},
			{ID: "archive", Cycle: contract.CycleStatus{ID: 3, Phase: contract.CycleWaiting, NextTickAt: earlier}},
		},
		PendingActions: []guiapp.PendingAction{{ID: "lock:1", Kind: "lock", RepoID: "docs", Label: "Zakładanie blokady", Phase: guiapp.ActionAwaitingProjection, StartedAt: started}},
	}
	got := projectViewModelAt(vm, started)
	if got.NextCycleAt != earlier || !got.CycleRunning || got.Repositories[0].Cycle.ID != 7 {
		t.Fatalf("cycle projection = %#v", got)
	}
	if len(got.PendingActions) != 1 || got.PendingActions[0].Phase != guiapp.ActionAwaitingProjection || got.PendingActions[0].StartedAt == "" {
		t.Fatalf("pending action projection = %#v", got.PendingActions)
	}
}

func TestPendingActionForTracksOnlyMutationsThatNeedProjectionBarrier(t *testing.T) {
	vm := guiapp.ViewModel{Repos: []guiapp.RepoViewModel{{ID: "docs", ServerID: "spot", ReservationCount: 2}}, Servers: []guiapp.ServerViewModel{{ID: "spot", ReservationsKnown: true}}}
	tracked := pendingActionFor(vm, ActionRequest{Kind: string(tray.IntentLock), RepoID: "docs"}, 12)
	if tracked.ID != "lock:12" || tracked.RepoID != "docs" || tracked.ServerID != "spot" || tracked.Label == "" || tracked.ReservationDelta != 1 || tracked.BaselineReservations != 2 || !tracked.BaselineReservationsKnown {
		t.Fatalf("tracked action = %#v", tracked)
	}
	if got := pendingActionFor(vm, ActionRequest{Kind: string(tray.IntentOpenFolder), RepoID: "docs"}, 13); got.ID != "" {
		t.Fatalf("presentation-only action was tracked = %#v", got)
	}
}

type recordingEmitter struct {
	name string
	data Snapshot
}

func (emitter *recordingEmitter) Emit(name string, data ...any) bool {
	emitter.name = name
	emitter.data = data[0].(Snapshot)
	return true
}

func TestOnChangeStoresAndEmitsSameRevision(t *testing.T) {
	emitter := &recordingEmitter{}
	service := &GUIService{snapshot: Snapshot{}, emitter: emitter}
	service.onChange(guiapp.ViewModel{Icon: guiapp.IconDisconnected})

	got := service.Snapshot()
	if got.Revision != 1 || emitter.name != snapshotEvent || emitter.data.Revision != got.Revision {
		t.Fatalf("snapshot=%+v event=%q/%+v", got, emitter.name, emitter.data)
	}
}

func TestTriggerTranslatesOnlyEligibleClosedSetActions(t *testing.T) {
	actions := make(chan tray.Intent, 1)
	service := &GUIService{
		actions: actions,
		view: guiapp.ViewModel{
			Connected: true,
			Capabilities: map[string]bool{
				contract.CapRepoLock:               true,
				contract.CapRepoReservationList:    true,
				contract.CapRepoReservationRelease: true,
				contract.CapSystemRestart:          true,
				contract.CapSystemShutdown:         true,
			},
			Servers: []guiapp.ServerViewModel{{ID: "server-1", RealmID: "realm-1", RealmAlias: "acme"}},
			Repos: []guiapp.RepoViewModel{{
				ID: "repo-1", ServerID: "server-1", Attached: true,
				Access: contract.AccessReadWrite, LocalPath: `E:\Projekt`,
			}},
			Reservations: []guiapp.Reservation{{
				ID: "opaque-row", ServerID: "server-1", RepoID: "repo-1",
				Path: "plan.dwg", Token: "secret-fencing-token", CanRelease: true,
			}},
		},
	}

	accepted := service.Trigger(ActionRequest{Kind: string(tray.IntentLock), RepoID: "repo-1"})
	if !accepted.Accepted {
		t.Fatalf("lock rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentLock || intent.RepoID != "repo-1" {
		t.Fatalf("intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentReleaseReservation), ReservationID: "opaque-row"})
	if !accepted.Accepted {
		t.Fatalf("reservation release rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentReleaseReservation || intent.ReservationID != "opaque-row" {
		t.Fatalf("reservation intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentRestartFileES)})
	if !accepted.Accepted {
		t.Fatalf("restart rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentRestartFileES {
		t.Fatalf("restart intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentShutdownFileES)})
	if !accepted.Accepted {
		t.Fatalf("shutdown rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentShutdownFileES {
		t.Fatalf("shutdown intent = %+v", intent)
	}
	if result := service.Trigger(ActionRequest{Kind: "delete_repository", RepoID: "repo-1"}); result.Accepted || result.Code != "action_unavailable" {
		t.Fatalf("unexpected action accepted: %+v", result)
	}
}

func TestTriggerRejectsLifecycleWithoutFreshCapability(t *testing.T) {
	actions := make(chan tray.Intent, 1)
	service := &GUIService{
		actions: actions,
		view: guiapp.ViewModel{
			Connected:    true,
			Stale:        true,
			Capabilities: map[string]bool{contract.CapSystemShutdown: true},
		},
	}
	if result := service.Trigger(ActionRequest{Kind: string(tray.IntentShutdownFileES)}); result.Accepted || result.Code != "action_unavailable" {
		t.Fatalf("stale shutdown accepted: %+v", result)
	}
	service.view.Stale = false
	service.view.Capabilities = map[string]bool{}
	if result := service.Trigger(ActionRequest{Kind: string(tray.IntentShutdownFileES)}); result.Accepted || result.Code != "action_unavailable" {
		t.Fatalf("uncapable shutdown accepted: %+v", result)
	}
}

func TestReservationProjectionNeverExposesFencingToken(t *testing.T) {
	vm := guiapp.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoReservationList: true, contract.CapRepoReservationRelease: true,
		},
		Servers: []guiapp.ServerViewModel{{ID: "server-1"}},
		Repos:   []guiapp.RepoViewModel{{ID: "repo-1", ServerID: "server-1", DisplayName: "Rysunki"}},
		Reservations: []guiapp.Reservation{{
			ID: "safe-row-id", ServerID: "server-1", RepoID: "repo-1", Path: "plan.dwg",
			Token: "never-send-this-token", OwnerLabel: "acme", CanRelease: true,
		}},
	}
	snapshot := projectViewModel(vm)
	if len(snapshot.Reservations) != 1 || snapshot.Reservations[0].ID != "safe-row-id" || !snapshot.Reservations[0].CanRelease {
		t.Fatalf("reservation projection = %+v", snapshot.Reservations)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "never-send-this-token") || strings.Contains(string(encoded), "token") {
		t.Fatalf("fencing token leaked into renderer JSON: %s", encoded)
	}
}
