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
	if result := service.Trigger(ActionRequest{Kind: "delete_repository", RepoID: "repo-1"}); result.Accepted || result.Code != "action_unavailable" {
		t.Fatalf("unexpected action accepted: %+v", result)
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
