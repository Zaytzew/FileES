package main

import (
	"reflect"
	"testing"
	"time"

	guiapp "filees/internal/gui/app"
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
