package main

import (
	"context"
	"errors"
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/journal"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestIntentAlertFollowsDaemonPendingEvenWhenStale(t *testing.T) {
	vm := guiapp.ViewModel{Connected: true, Repos: []guiapp.RepoViewModel{{ID: "docs", Attached: true, Pending: contract.PendingStats{RenameUncertain: 1}}}}
	for _, stale := range []bool{false, true} {
		vm.Stale = stale
		if !projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
			t.Fatal("decision disappeared without resolution")
		}
	}
	vm.Repos[0].Pending.RenameUncertain = 0
	if projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
		t.Fatal("resolved decision remains")
	}
	vm.Repos[0].Pending.RenameUncertain = 1
	vm.Repos[0].ServerDeleted = true
	if projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
		t.Fatal("deleted repository asks for publication")
	}
	vm.Repos[0].ServerDeleted = false
	vm.Repos[0].Attached = false
	if projectViewModel(vm, journal.Texts{}).Repositories[0].IntentResolutionRequired {
		t.Fatal("remote repository asks for local resolution")
	}
}

func TestIntentResolutionFolderActionProjection(t *testing.T) {
	request := platform.SettingsDialogRequest{FocusRepoID: "docs", Servers: []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{ID: "docs", CanResolveIntents: true}}}}}
	projection, ok := projectRepositorySettings(request)
	if !ok || len(projection.Actions) != 1 || projection.Actions[0].ID != string(platform.SettingsDialogResolveIntents) {
		t.Fatalf("projection=%+v", projection)
	}
	request.Servers[0].Folders[0].CanResolveIntents = false
	projection, ok = projectRepositorySettings(request)
	if !ok || len(projection.Actions) != 0 {
		t.Fatal("renderer invented permission")
	}
}

func TestCommitRecoveryFolderActionProjection(t *testing.T) {
	request := platform.SettingsDialogRequest{FocusRepoID: "docs", Servers: []platform.SettingsServer{{ID: "spot", Folders: []platform.SettingsFolder{{ID: "docs", CanResolveCommitRecovery: true}}}}}
	projection, ok := projectRepositorySettings(request)
	if !ok || len(projection.Actions) != 1 || projection.Actions[0].ID != string(platform.SettingsDialogResolveCommitRecovery) {
		t.Fatalf("projection=%+v", projection)
	}
}

type recoveryPlanClient struct {
	intentResolutionClient
	plan *contract.CommitRecoveryPlan
	err  error
}

func (c recoveryPlanClient) RepoCommitRecoveryPlan(context.Context, string) (*contract.CommitRecoveryPlan, error) {
	return c.plan, c.err
}

func TestRecoveryPlanAdapterChecksRepositoryAndCopiesProof(t *testing.T) {
	wire := &contract.CommitRecoveryPlan{PlanID: "plan", RepoID: "docs", TransactionID: "attempt", Choice: contract.CommitRecoveryServerCopy, FirstRevision: 44, HeadRevision: 43, Paths: []string{"file.pdf"}, Conflicts: []string{"file.pdf"}, ConflictCopy: "!kolizje/conflicted-copy-plan"}
	plan, err := (intentResolverAdapter{client: recoveryPlanClient{plan: wire}}).PlanCommitRecovery(context.Background(), "docs")
	if err != nil || plan == nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if plan.PlanID != wire.PlanID || plan.RepoID != wire.RepoID || plan.TransactionID != wire.TransactionID || plan.Choice != wire.Choice || plan.FirstRevision != 44 || plan.HeadRevision != 43 || len(plan.Paths) != 1 || plan.Paths[0] != "file.pdf" {
		t.Fatalf("proof changed: %+v", plan)
	}
	wire.Paths[0] = "mutated"
	wire.Conflicts[0] = "mutated"
	if plan.Conflicts[0] != "file.pdf" || plan.ConflictCopy != wire.ConflictCopy {
		t.Fatal("conflict proof lost or aliased")
	}
	if plan.Paths[0] != "file.pdf" {
		t.Fatal("presentation shares mutable transport data")
	}
	for _, invalid := range []*contract.CommitRecoveryPlan{nil, {RepoID: "other"}} {
		if got, err := (intentResolverAdapter{client: recoveryPlanClient{plan: invalid}}).PlanCommitRecovery(context.Background(), "docs"); err == nil || got != nil {
			t.Fatalf("invalid response accepted: %+v %v", got, err)
		}
	}
	failure := errors.New("transport unavailable")
	if got, err := (intentResolverAdapter{client: recoveryPlanClient{err: failure}}).PlanCommitRecovery(context.Background(), "docs"); !errors.Is(err, failure) || got != nil {
		t.Fatalf("transport failure lost: %+v %v", got, err)
	}
}
