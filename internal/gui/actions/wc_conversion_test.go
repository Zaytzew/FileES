package actions

import (
	"context"
	"errors"
	"filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/platform/platformtest"
	contract "filees/pkg/contract/v1"
	"testing"
)

type conversionGUIProbe struct {
	calls []contract.WCConversionPayload
	err   error
}

func (f *conversionGUIProbe) WCConversion(_ context.Context, p contract.WCConversionPayload) (*contract.WCConversionStatus, error) {
	f.calls = append(f.calls, p)
	return &contract.WCConversionStatus{ServerID: p.ServerID, RepoID: p.RepoID, SourcePath: "fixture", OperationID: "operation"}, f.err
}
func conversionGUIView() app.ViewModel {
	r := app.RepoViewModel{ID: "repo", ServerID: "lab", Attached: true, LocalPath: "fixture", State: contract.StateActive,
		Conversion: &contract.WCConversionStatus{ServerID: "lab", RepoID: "repo", SourcePath: "fixture", CanBegin: true}}
	return app.ViewModel{Connected: true, Capabilities: map[string]bool{contract.CapWCConversion: true},
		VirtualFolders: &contract.VirtualFoldersStatus{Servers: map[string]bool{"lab": true}}, Repos: []app.RepoViewModel{r}}
}
func TestWCConversionGUIAsksRechecksAndNeverRetriesLostReply(t *testing.T) {
	for _, mode := range []string{"accept", "decline", "stale-path", "stale-capability", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			vm := conversionGUIView()
			probe := &conversionGUIProbe{}
			if mode == "lost-ack" {
				probe.err = errors.New("connection lost")
			}
			fake := &platformtest.Fake{ConfirmFunc: func(context.Context, platform.ConfirmRequest) (bool, error) {
				if mode == "stale-path" {
					vm.Repos[0].LocalPath = "different"
				}
				if mode == "stale-capability" {
					delete(vm.Capabilities, contract.CapWCConversion)
				}
				return mode != "decline", nil
			}}
			c := New(Config{ViewModel: func() app.ViewModel { return vm }, RepositoryConverter: probe, Prompter: fake, Notifier: fake})
			c.startWCConversion(t.Context(), "lab", "repo", "begin")
			c.tasks.Wait()
			want := 0
			if mode == "accept" || mode == "lost-ack" {
				want = 1
			}
			if len(probe.calls) != want || len(fake.Snapshot().ConfirmRequests) != 1 {
				t.Fatal(mode, probe.calls, fake.Snapshot())
			}
			if want > 0 && (probe.calls[0].SourcePath != "fixture" || probe.calls[0].OperationID != "") {
				t.Fatal(probe.calls)
			}
		})
	}
}
func TestWCConversionGUINoCapabilityNoOptionAndCleanupNoRollback(t *testing.T) {
	vm := conversionGUIView()
	if !conversionAllowed(vm, vm.Repos[0], "begin") {
		t.Fatal("ordinary enabled copy refused")
	}
	delete(vm.Capabilities, contract.CapWCConversion)
	if conversionAllowed(vm, vm.Repos[0], "begin") {
		t.Fatal("old daemon exposed action")
	}
	vm.Capabilities[contract.CapWCConversion] = true
	vm.Repos[0].Sparse = true
	if conversionAllowed(vm, vm.Repos[0], "begin") {
		t.Fatal("already sparse action")
	}
	vm.Repos[0].Conversion = &contract.WCConversionStatus{ServerID: "lab", RepoID: "repo", SourcePath: "fixture", OperationID: "id", Phase: "cleanup", Error: "busy", CanRetry: true}
	if conversionAllowed(vm, vm.Repos[0], "rollback") || !conversionAllowed(vm, vm.Repos[0], "retry") {
		t.Fatal("cleanup controls")
	}
	if vm.Repos[0].DisplayState() != app.RepoDisplayAttention {
		t.Fatal("blocked conversion not visible")
	}
	vm.Repos[0].ServerDeleted, vm.Repos[0].State = true, "deleted"
	if vm.Repos[0].DisplayState() != app.RepoDisplayDeleted {
		t.Fatal("stale conversion overrode server deletion")
	}
}
