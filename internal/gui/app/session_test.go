package app

import (
	"context"
	contract "filees/pkg/contract/v1"
	"testing"
	"time"
)

func TestSessionSnapshotReachesGUIWithoutAlarm(t *testing.T) {
	d := &fakeDaemon{activity: func(context.Context, int) (*contract.RepoActivityResult, error) {
		return &contract.RepoActivityResult{Sessions: []contract.SessionRecord{{ID: "session", Phase: "started", Version: "test", At: "2026-10-16T00:00:00Z"}}}, nil
	}}
	d.hello = func(context.Context) (*contract.HelloResult, error) {
		return &contract.HelloResult{DaemonVersion: "test", ProtocolVersions: []string{contract.Protocol}, Capabilities: []string{contract.CapRepoActivity}}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	vc := newVMCollector()
	startApp(ctx, d, vc, newFakeClock(), &fakeBackoff{steps: []time.Duration{time.Hour}})
	vm := vc.waitFor(t, 5*time.Second, func(vm ViewModel) bool { return len(vm.Sessions) == 1 })
	if vm.Sessions[0].ID != "session" || len(vm.Errors) != 0 || len(vm.Activity) != 0 || vm.Icon != IconActive {
		t.Fatal("session became work/error", vm)
	}
}
