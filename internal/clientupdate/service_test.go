package clientupdate

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	if err != nil || status.State != "restart_required" || !status.RestartRequired {
		t.Fatalf("post-apply status = %+v, %v", status, err)
	}
	service.Resolver = resolverStub{resolved: resolvedRelease(1, "r1", "0.9")}
	// A new process verifies channels again; the old process only reports its
	// already verified pending restart, without another installation.
	service.appliedRestart = false
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

func TestNewerLocalBuildDoesNotOfferOrApplyOlderChannelRelease(t *testing.T) {
	installer := &installerStub{}
	service := &Service{
		Resolver:  resolverStub{resolved: resolvedRelease(868, "r868", "0.1.15.868")},
		Installer: installer, State: StateStore{Path: filepath.Join(t.TempDir(), "update.json")},
		CurrentVersion: "0.1.15+r883",
	}
	status, err := service.Status(context.Background())
	if err != nil || status.State != "current" || status.CurrentVersion != "0.1.15.883" || status.AvailableVersion != "" || status.RestartRequired {
		t.Fatalf("newer local status = %+v, %v", status, err)
	}
	plan, err := service.Plan(context.Background())
	if err != nil || len(plan.Changes) != 0 || plan.RestartRequired || installer.planCalls != 0 {
		t.Fatalf("newer local plan = %+v, calls=%d, %v", plan, installer.planCalls, err)
	}
	result, err := service.Apply(context.Background())
	if err != nil || result.InstalledVersion != "0.1.15.883" || result.RestartRequired || installer.applyCalls != 0 {
		t.Fatalf("newer local apply = %+v, calls=%d, %v", result, installer.applyCalls, err)
	}
}

func TestNumericClientVersionComparisonPadsMissingComponents(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{left: "1.0", right: "1.0.0", want: 0},
		{left: "1.0.1", right: "1", want: 1},
		{left: "0.1.15.9", right: "0.1.15.10", want: -1},
		{left: "0.01.015.010", right: "0.1.15.10", want: 0},
	} {
		got, ok := compareNumericClientVersions(test.left, test.right)
		if !ok || got != test.want {
			t.Errorf("compareNumericClientVersions(%q, %q) = %d, %v; want %d, true", test.left, test.right, got, ok, test.want)
		}
	}
	if _, ok := compareNumericClientVersions("alpha", "1.0"); ok {
		t.Fatal("named client version was treated as numerically comparable")
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
	if err != nil || status.State != "restart_required" || !status.RestartRequired {
		t.Fatalf("post-repair status = %+v, %v", status, err)
	}
}

func TestRestartLatchSurvivesOfflineReadsAndRepeatedApplyUntilNewProcess(t *testing.T) {
	ctx := context.Background()
	installer := &installerStub{}
	store := StateStore{Path: filepath.Join(t.TempDir(), "state.json")}
	resolver := resolverStub{resolved: resolvedRelease(970, "r970", "0.1.15.970")}
	service := &Service{Resolver: resolver, Installer: installer, State: store, CurrentVersion: "0.1.15+r967"}
	if _, err := service.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	service.Resolver = resolverStub{err: errors.New("offline")}
	for range 3 {
		status, err := service.Status(ctx)
		if err != nil || status.State != "restart_required" || !status.RestartRequired || status.CurrentVersion != "0.1.15.967" || status.AvailableVersion != "0.1.15.970" {
			t.Fatalf("status=%+v, %v", status, err)
		}
		plan, err := service.Plan(ctx)
		if err != nil || !plan.RestartRequired || len(plan.Changes) != 0 {
			t.Fatalf("plan=%+v, %v", plan, err)
		}
		result, err := service.Apply(ctx)
		if err != nil || !result.RestartRequired || installer.applyCalls != 1 {
			t.Fatalf("apply=%+v, calls=%d, %v", result, installer.applyCalls, err)
		}
	}
	restarted := &Service{Resolver: resolver, Installer: installer, State: store, CurrentVersion: "0.1.15+r970"}
	status, err := restarted.Status(ctx)
	if err != nil || status.RestartRequired || status.State != "current" {
		t.Fatalf("restarted=%+v, %v", status, err)
	}
}

type countingResolver struct {
	resolved *releaseenvelope.Resolved
	calls    int
}

func (r *countingResolver) Resolve(context.Context, string, string, string) (*releaseenvelope.Resolved, error) {
	r.calls++
	return r.resolved, nil
}

// Owner's production, 2026-09-25: every repo/system status read the channel
// four times behind the service mutex, and the interface lost the daemon in a
// cycle whenever the release server was slow. The verified answer is reused
// for a while, and a busy channel is not waited for.
func TestStatusReusesTheVerifiedAnswerAndDoesNotQueueBehindTheChannel(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	resolver := &countingResolver{resolved: resolvedRelease(2, "r2", "1.1")}
	service := &Service{Resolver: resolver, Installer: &installerStub{}, State: StateStore{Path: filepath.Join(t.TempDir(), "update.json")}, CurrentVersion: "1.0", Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		if status, err := service.Status(context.Background()); err != nil || status.State != "available" {
			t.Fatalf("status %d = %+v, %v", i, status, err)
		}
	}
	if resolver.calls != 1 {
		t.Fatalf("channel read %d times within the cache period", resolver.calls)
	}
	now = now.Add(statusCacheTTL)
	service.mu.Lock() // a plan or installation holds the channel
	status, err := service.Status(context.Background())
	service.mu.Unlock()
	if err != nil || status.State != "available" || resolver.calls != 1 {
		t.Fatalf("busy channel: status=%+v err=%v calls=%d", status, err, resolver.calls)
	}
	// Expired: answered at once from memory, refreshed in the background.
	if _, err := service.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.refreshes.Wait()
	if resolver.calls != 2 {
		t.Fatalf("expired answer was not refreshed: calls=%d", resolver.calls)
	}
}

// Owner, 2026-09-25: with r1558's five-minute answer a release published
// while the pair ran showed only after a restart. It shows on the first
// status after the answer's short lifetime, without anyone waiting on the
// channel.
func TestANewReleaseShowsWithoutRestartingThePair(t *testing.T) {
	now := time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)
	resolver := &countingResolver{resolved: resolvedRelease(2, "r2", "1.0")}
	service := &Service{Resolver: resolver, Installer: &installerStub{}, State: StateStore{Path: filepath.Join(t.TempDir(), "update.json")}, CurrentVersion: "1.0", Now: func() time.Time { return now }}
	if status, err := service.Status(context.Background()); err != nil || status.State != "current" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	resolver.resolved = resolvedRelease(3, "r3", "1.1") // published meanwhile
	now = now.Add(statusCacheTTL)
	if status, _ := service.Status(context.Background()); status.State != "current" {
		t.Fatalf("expired answer should be returned at once: %+v", status)
	}
	service.refreshes.Wait()
	if status, _ := service.Status(context.Background()); status.State != "available" || status.AvailableVersion != "1.1" {
		t.Fatalf("new release not seen: %+v", status)
	}
	if statusCacheTTL > time.Minute {
		t.Fatalf("answer lifetime %v hides a release for too long", statusCacheTTL)
	}
}
