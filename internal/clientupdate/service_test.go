package clientupdate

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"filees/internal/releaseenvelope"
	contract "filees/pkg/contract/v1"
)

type resolverStub struct {
	resolved *releaseenvelope.Resolved
	err      error
}

func (stub resolverStub) Resolve(context.Context, string, string, string) (*releaseenvelope.Resolved, error) {
	return stub.resolved, stub.err
}

type installerStub struct {
	planCalls, applyCalls int
	applyErr              error
}

func (stub *installerStub) Plan(context.Context, *releaseenvelope.Resolved) ([]contract.UpdateChange, bool, error) {
	stub.planCalls++
	return []contract.UpdateChange{{Action: "update", Path: "filees-gui"}}, true, nil
}

func (stub *installerStub) Apply(context.Context, *releaseenvelope.Resolved) error {
	stub.applyCalls++
	return stub.applyErr
}

func resolvedRelease(sequence uint64, releaseID, version string) *releaseenvelope.Resolved {
	return &releaseenvelope.Resolved{
		Envelope:     &releaseenvelope.Envelope{ReleaseID: releaseID, Sequence: sequence, SecurityEpoch: 1},
		Manifest:     &releaseenvelope.ArtifactManifest{ReleaseID: releaseID, Sequence: sequence, SecurityEpoch: 1, Version: version},
		SigningKeyID: "key-1",
	}
}

func TestServiceStatusPlanApplyAndPersistAntiRollback(t *testing.T) {
	installer := &installerStub{}
	store := StateStore{Path: filepath.Join(t.TempDir(), "update.json")}
	service := &Service{Resolver: resolverStub{resolved: resolvedRelease(2, "r2", "1.1")}, Installer: installer, State: store, Channel: "alpha", ChannelPath: "channels/alpha.v2.json", Component: "desktop", Platform: "linux-amd64", CurrentVersion: "1.0"}
	status, err := service.Status(context.Background())
	if err != nil || status.State != "available" || status.Channel != "alpha" || status.AvailableVersion != "1.1" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	plan, err := service.Plan(context.Background())
	if err != nil || len(plan.Changes) != 1 || !plan.RestartRequired {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	result, err := service.Apply(context.Background())
	if err != nil || result.InstalledVersion != "1.1" || !result.RestartRequired || installer.applyCalls != 1 {
		t.Fatalf("apply = %+v, calls=%d, %v", result, installer.applyCalls, err)
	}
	state, err := store.Load()
	if err != nil || state.HighestSequence != 2 || state.InstalledVersion != "1.1" {
		t.Fatalf("state = %+v, %v", state, err)
	}
	status, err = service.Status(context.Background())
	if err != nil || status.State != "current" {
		t.Fatalf("post-apply status = %+v, %v", status, err)
	}
	service.Resolver = resolverStub{resolved: resolvedRelease(1, "r1", "0.9")}
	if _, err := service.Status(context.Background()); err == nil {
		t.Fatal("service accepted signed rollback")
	}
}

func TestServiceDoesNotAdvanceStateWhenInstallerFails(t *testing.T) {
	installer := &installerStub{applyErr: errors.New("payload hash mismatch")}
	store := StateStore{Path: filepath.Join(t.TempDir(), "update.json")}
	service := &Service{Resolver: resolverStub{resolved: resolvedRelease(3, "r3", "1.2")}, Installer: installer, State: store, CurrentVersion: "1.0"}
	if _, err := service.Apply(context.Background()); err == nil {
		t.Fatal("apply succeeded despite installer failure")
	}
	state, err := store.Load()
	if err != nil || state.HighestSequence != 0 {
		t.Fatalf("failed apply advanced state: %+v, %v", state, err)
	}
}

func TestBuildStampAndDistributionVersionAreEquivalent(t *testing.T) {
	installer := &installerStub{}
	service := &Service{
		Resolver:  resolverStub{resolved: resolvedRelease(850, "r850", "0.1.15.850")},
		Installer: installer, State: StateStore{Path: filepath.Join(t.TempDir(), "update.json")},
		CurrentVersion: "0.1.15+r850",
	}
	status, err := service.Status(context.Background())
	if err != nil || status.State != "current" || status.CurrentVersion != "0.1.15.850" || status.AvailableVersion != "" {
		t.Fatalf("equivalent version status = %+v, %v", status, err)
	}
	result, err := service.Apply(context.Background())
	if err != nil || installer.applyCalls != 0 || result.InstalledVersion != "0.1.15.850" {
		t.Fatalf("equivalent version apply = %+v, calls=%d, %v", result, installer.applyCalls, err)
	}
}

func TestOldMSIRepairsItselfWithoutLoweringHighWater(t *testing.T) {
	installer := &installerStub{}
	store := StateStore{Path: filepath.Join(t.TempDir(), "update.json")}
	if err := store.Save(State{HighestSequence: 3, SecurityEpoch: 1, ReleaseID: "r3", InstalledVersion: "1.2"}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		Resolver:  resolverStub{resolved: resolvedRelease(3, "r3", "1.2")},
		Installer: installer, State: store, CurrentVersion: "1.0",
	}
	status, err := service.Status(context.Background())
	if err != nil || status.State != "available" || status.CurrentVersion != "1.0" || status.AvailableVersion != "1.2" {
		t.Fatalf("repaired MSI status = %+v, %v", status, err)
	}
	if _, err := service.Apply(context.Background()); err != nil || installer.applyCalls != 1 {
		t.Fatalf("repair apply calls=%d, err=%v", installer.applyCalls, err)
	}
	state, err := store.Load()
	if err != nil || state.HighestSequence != 3 || state.ReleaseID != "r3" || state.InstalledVersion != "1.2" {
		t.Fatalf("repair lowered or corrupted high-water: %+v, %v", state, err)
	}
	status, err = service.Status(context.Background())
	if err != nil || status.State != "current" {
		t.Fatalf("post-repair status = %+v, %v", status, err)
	}
}
