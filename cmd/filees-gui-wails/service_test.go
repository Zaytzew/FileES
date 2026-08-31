package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	guiapp "filees/internal/gui/app"
	"filees/internal/gui/tray"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
)

func TestProjectViewModelKeepsRendererOnPresentationBoundary(t *testing.T) {
	operation := "commit"
	refreshed := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	vm := guiapp.ViewModel{
		Connected: true, DaemonState: "running", UptimeSec: 42,
		LastRefresh: refreshed, Icon: guiapp.IconBusy,
		Capabilities: map[string]bool{"repo.status": true, "ignored": false, "repo.list": true, contract.CapRepoPublish: true, contract.CapNoticeAck: true},
		Repos: []guiapp.RepoViewModel{{
			ID: "repo-1", ServerID: "server-1", DisplayName: "Projekt",
			LocalPath: `E:\Projekt`, Attached: true, Access: contract.AccessReadWrite,
			State: contract.StateActive, Connectivity: contract.ConnOnline,
			LocalRev: 7, HeadRev: 8, CurrentOp: &operation,
			WorkingCopyBytes: 8192, WorkingCopySizeKnown: true,
			Pending: contract.PendingStats{Added: 1, Modified: 2, Deleted: 3, TotalBytes: 4096},
		}},
		Servers: []guiapp.ServerViewModel{{ID: "server-1", DisplayName: "Spot"}},
		Notices: []guiapp.NoticeViewModel{{ID: "notice-1", RepoID: "repo-1", Revision: 8, Title: "Wydanie r8", CreatedAt: refreshed.Format(time.RFC3339)}},
	}

	got := projectViewModel(vm)
	if !got.Connected || got.IconState != "busy" || got.LastRefresh != "2026-08-23T10:00:00Z" {
		t.Fatalf("unexpected top-level projection: %+v", got)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"notice.ack", "repo.list", "repo.publish", "repo.status"}) {
		t.Fatalf("capabilities = %#v", got.Capabilities)
	}
	if len(got.Repositories) != 1 {
		t.Fatalf("repositories = %#v", got.Repositories)
	}
	repo := got.Repositories[0]
	if repo.PendingFiles != 6 || repo.PendingBytes != 4096 || repo.WorkingCopyBytes != 8192 || !repo.WorkingCopySizeKnown || repo.CurrentOperation != "commit" || repo.DisplayState != "busy" || !repo.CanPublish {
		t.Fatalf("repository projection = %+v", repo)
	}
	if len(got.Notices) != 1 || !got.Notices[0].CanAck || got.Notices[0].Revision != 8 || got.Notices[0].Title != "Wydanie r8" {
		t.Fatalf("shout projection = %+v", got.Notices)
	}
}

func TestProjectViewModelBuildsAggregatePublicShareCardAndClosedSetIntents(t *testing.T) {
	vm := guiapp.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoPublicShareList: true, contract.CapRepoPublicShareCreate: true,
			contract.CapRepoPublicShareUpdate: true, contract.CapRepoPublicShareRevoke: true,
			contract.CapRepoPublicShareDelete: true,
		},
		Servers:           []guiapp.ServerViewModel{{ID: "spot", DisplayName: "Spot"}},
		Repos:             []guiapp.RepoViewModel{{ID: "docs", ServerID: "spot", DisplayName: "Dokumenty"}},
		PublicSharesKnown: true,
		PublicShares: []guiapp.PublicShareViewModel{
			{ChannelID: "old", ServerID: "spot", RepoID: "docs", Alias: "acme", Slug: "old", State: "revoked", UpdatedAt: "2026-08-29T09:00:00Z"},
			{ChannelID: "live", ServerID: "spot", RepoID: "docs", RepoDisplayName: "Dokumenty", Alias: "acme", Slug: "release", State: "active", UpdatedAt: "2026-08-29T08:00:00Z", RecipientCount: 2, ObjectCount: 7, PasswordProtected: true, FollowHead: true},
		},
	}

	got := projectViewModel(vm)
	if !got.PublicSharesKnown || len(got.PublicShares) != 2 || got.PublicShares[0].ChannelID != "live" {
		t.Fatalf("public share projection = %#v", got.PublicShares)
	}
	live := got.PublicShares[0]
	if live.Address != "acme/release" || live.Repository != "Dokumenty" || !live.CanOpen || !live.CanRevoke || !live.FollowHead || live.ObjectCount != 7 {
		t.Fatalf("active share projection = %#v", live)
	}
	intent, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentManagePublicShares), ServerID: "spot", RepoID: "docs", ChannelID: "live"})
	if !allowed || intent.Kind != tray.IntentManagePublicShares || intent.ChannelID != "live" {
		t.Fatalf("manage intent = %#v allowed=%v", intent, allowed)
	}
	intent, allowed = translateAction(vm, ActionRequest{Kind: string(tray.IntentRevokePublicShare), ServerID: "spot", RepoID: "docs", ChannelID: "live"})
	if !allowed || intent.Kind != tray.IntentRevokePublicShare || intent.ChannelID != "live" {
		t.Fatalf("revoke intent = %#v allowed=%v", intent, allowed)
	}
	if _, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentRevokePublicShare), ServerID: "spot", RepoID: "docs", ChannelID: "old"}); allowed {
		t.Fatal("revoked public share accepted another revoke")
	}
	if _, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentManagePublicShares), ServerID: "spot", RepoID: "docs", ChannelID: "forged"}); allowed {
		t.Fatal("unprojected public share accepted")
	}
	intent, allowed = translateAction(vm, ActionRequest{Kind: string(tray.IntentRevokePublicShares), ServerID: "spot", ChannelIDs: []string{"live", "live"}})
	if !allowed || intent.Kind != tray.IntentRevokePublicShares || !reflect.DeepEqual(intent.ChannelIDs, []string{"live"}) {
		t.Fatalf("bulk revoke intent = %#v allowed=%v", intent, allowed)
	}
	if _, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentRevokePublicShares), ServerID: "spot", ChannelIDs: []string{"live", "old"}}); allowed {
		t.Fatal("bulk revoke accepted inactive share")
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

func TestQuarantineProjectionAndDirectIntentRemainOwnerScoped(t *testing.T) {
	vm := guiapp.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoQuarantineList: true, contract.CapRepoQuarantineHide: true, contract.CapRepoQuarantineFetch: true,
		},
		Servers: []guiapp.ServerViewModel{{ID: "spot", RealmID: "realm-own"}},
		Repos: []guiapp.RepoViewModel{
			{ID: "own-trash", ServerID: "spot", OwnerRealmID: "realm-own", Purpose: clientview.PurposeUploadTrash},
			{ID: "guest-trash", ServerID: "spot", OwnerRealmID: "realm-guest", Purpose: clientview.PurposeUploadTrash},
			{ID: "ordinary", ServerID: "spot", OwnerRealmID: "realm-own"},
		},
	}
	projected := projectViewModel(vm)
	if !projected.Repositories[0].CanReviewQuarantine || projected.Repositories[1].CanReviewQuarantine || projected.Repositories[2].CanReviewQuarantine {
		t.Fatalf("quarantine action projection = %+v", projected.Repositories)
	}
	intent, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentReviewQuarantine), RepoID: "own-trash"})
	if !allowed || intent.Kind != tray.IntentReviewQuarantine || intent.ServerID != "spot" || intent.RepoID != "own-trash" {
		t.Fatalf("direct quarantine intent = %+v allowed=%v", intent, allowed)
	}
	for _, repoID := range []string{"guest-trash", "ordinary"} {
		if _, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentReviewQuarantine), RepoID: repoID}); allowed {
			t.Fatalf("direct quarantine intent accepted %s", repoID)
		}
	}
	delete(vm.Capabilities, contract.CapRepoQuarantineFetch)
	if _, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentReviewQuarantine), RepoID: "own-trash"}); allowed {
		t.Fatal("direct quarantine intent accepted an incomplete capability set")
	}
}

func TestDeletedRepositoryProjectsRetentionAndRecoveryIntent(t *testing.T) {
	vm := guiapp.ViewModel{
		Servers: []guiapp.ServerViewModel{{ID: "spot"}},
		Repos: []guiapp.RepoViewModel{{
			ID: "gone", ServerID: "spot", DisplayName: "Archiwum", State: "deleted",
			ServerDeleted: true, LocalCleanupPending: true,
			RetainUntil: "2026-09-22T12:00:00Z", RecoveryOperationID: "delete-op", RecoveryAvailable: true,
		}},
	}
	projected := projectViewModel(vm)
	if len(projected.Repositories) != 1 {
		t.Fatalf("repositories=%#v", projected.Repositories)
	}
	repo := projected.Repositories[0]
	if repo.DisplayState != "deleted" || !repo.ServerDeleted || !repo.LocalCleanupPending || !repo.RecoveryAvailable || repo.RecoveryOperationID != "delete-op" {
		t.Fatalf("deleted projection=%+v", repo)
	}
	intent, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentDownloadRecovery), RepoID: "gone"})
	if !allowed || intent.Kind != tray.IntentDownloadRecovery || intent.RecoveryOperationID != "delete-op" || intent.ServerID != "spot" {
		t.Fatalf("recovery intent=%+v allowed=%v", intent, allowed)
	}
}

func TestGlobalPairingIntentUsesActiveProjectionAsPlaceholder(t *testing.T) {
	vm := guiapp.ViewModel{
		Connected:    true,
		Capabilities: map[string]bool{contract.CapMobilePairingBegin: true},
		Servers:      []guiapp.ServerViewModel{{ID: "spot"}, {ID: "archive"}},
	}
	intent, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentPairMobileDevice)})
	if !allowed || intent.Kind != tray.IntentPairMobileDevice || intent.ServerID != "spot" {
		t.Fatalf("pairing intent=%+v allowed=%v", intent, allowed)
	}
	vm.Stale = true
	if _, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentPairMobileDevice)}); allowed {
		t.Fatal("stale projection allowed mobile pairing")
	}
}

func TestUnattachedStatePillTranslatesToDirectAttach(t *testing.T) {
	vm := guiapp.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapRepoAttachIntent:  true,
			contract.CapRepoAttachApprove: true,
		},
		Servers: []guiapp.ServerViewModel{{ID: "spot"}},
		Repos: []guiapp.RepoViewModel{{
			ID: "docs", ServerID: "spot", State: contract.StateUnattached,
		}},
	}
	intent, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentAttachRepository), RepoID: "docs"})
	if !allowed || intent.Kind != tray.IntentAttachRepository || intent.RepoID != "docs" || intent.ServerID != "spot" {
		t.Fatalf("attach intent=%+v allowed=%v", intent, allowed)
	}
	vm.Repos[0].Attached = true
	vm.Repos[0].LocalPath = "/wc/docs"
	if _, allowed := translateAction(vm, ActionRequest{Kind: string(tray.IntentAttachRepository), RepoID: "docs"}); allowed {
		t.Fatal("attached repository accepted direct attach")
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
				contract.CapRepoPublish:            true,
				contract.CapNoticeAck:              true,
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
			Notices: []guiapp.NoticeViewModel{{ID: "notice-1", RepoID: "repo-1", Title: "Wydanie r8"}},
		},
	}

	accepted := service.Trigger(ActionRequest{Kind: string(tray.IntentActivate)})
	if !accepted.Accepted {
		t.Fatalf("activation rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentActivate {
		t.Fatalf("activation intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentLock), RepoID: "repo-1"})
	if !accepted.Accepted {
		t.Fatalf("lock rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentLock || intent.RepoID != "repo-1" {
		t.Fatalf("intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentPublish), RepoID: "repo-1"})
	if !accepted.Accepted {
		t.Fatalf("publish rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentPublish || intent.RepoID != "repo-1" {
		t.Fatalf("publish intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentAckNotice), NoticeID: "notice-1"})
	if !accepted.Accepted {
		t.Fatalf("notice ack rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentAckNotice || intent.NoticeID != "notice-1" {
		t.Fatalf("notice intent = %+v", intent)
	}
	service.view.Notices[0].Acked = true
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentAckNotice), NoticeID: "notice-1"})
	if accepted.Accepted {
		t.Fatalf("acknowledged notice accepted again: %+v", accepted)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentReleaseReservation), ReservationID: "opaque-row"})
	if !accepted.Accepted {
		t.Fatalf("reservation release rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentReleaseReservation || intent.ReservationID != "opaque-row" {
		t.Fatalf("reservation intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentSettings), ServerID: "server-1"})
	if !accepted.Accepted {
		t.Fatalf("settings rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentSettings || intent.ServerID != "server-1" {
		t.Fatalf("settings intent = %+v", intent)
	}
	accepted = service.Trigger(ActionRequest{Kind: string(tray.IntentSettings), ServerID: "server-1", RepoID: "repo-1"})
	if !accepted.Accepted {
		t.Fatalf("repository settings rejected: %+v", accepted)
	}
	if intent := <-actions; intent.Kind != tray.IntentSettings || intent.ServerID != "server-1" || intent.RepoID != "repo-1" {
		t.Fatalf("repository settings intent = %+v", intent)
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

func TestLockReleaseProjectionLinksForeignReservationWithoutExposingToken(t *testing.T) {
	vm := guiapp.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapLockReleaseRequest: true, contract.CapLockReleaseDismiss: true, contract.CapLockReleaseAccept: true,
		},
		Repos:        []guiapp.RepoViewModel{{ID: "docs", ServerID: "spot", DisplayName: "Rysunki"}},
		Reservations: []guiapp.Reservation{{ID: "safe-row", ServerID: "spot", RepoID: "docs", Path: "plans/a.dwg", Token: "opaque-token", CanRelease: false}},
		LockReleaseRequests: []guiapp.LockReleaseRequest{{
			ID: "request-1", ServerID: "spot", RepoID: "docs", Path: "plans/a.dwg", ObservedLockID: "opaque-token",
			Role: "requester", CounterpartyRealmAlias: "studio", State: "pending",
		}},
	}
	snapshot := projectViewModel(vm)
	if len(snapshot.Reservations) != 1 || snapshot.Reservations[0].LockReleaseRequestID != "request-1" || snapshot.Reservations[0].LockReleaseState != "pending" || snapshot.Reservations[0].CanRequestRelease {
		t.Fatalf("reservation request projection=%+v", snapshot.Reservations)
	}
	if len(snapshot.LockReleaseRequests) != 1 || snapshot.LockReleaseRequests[0].Repository != "Rysunki" {
		t.Fatalf("request projection=%+v", snapshot.LockReleaseRequests)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "opaque-token") || strings.Contains(string(encoded), "ObservedLockID") {
		t.Fatalf("fencing token escaped into Wails JSON: %s", encoded)
	}
}

func TestTranslateLockReleaseActionsRevalidatesRoleAndFence(t *testing.T) {
	vm := guiapp.ViewModel{
		Connected: true,
		Capabilities: map[string]bool{
			contract.CapLockReleaseRequest: true, contract.CapLockReleaseDismiss: true, contract.CapLockReleaseAccept: true,
		},
		Reservations:        []guiapp.Reservation{{ID: "safe-row", ServerID: "spot", RepoID: "docs", Path: "a.dwg", Token: "token"}},
		LockReleaseRequests: []guiapp.LockReleaseRequest{{ID: "holder-request", ServerID: "spot", RepoID: "docs", Path: "b.dwg", Role: "holder", State: "pending"}},
	}
	intent, ok := translateAction(vm, ActionRequest{Kind: string(tray.IntentRequestLockRelease), ReservationID: "safe-row"})
	if !ok || intent.Kind != tray.IntentRequestLockRelease || intent.ReservationID != "safe-row" {
		t.Fatalf("request intent=%+v ok=%v", intent, ok)
	}
	intent, ok = translateAction(vm, ActionRequest{Kind: string(tray.IntentAcceptLockRelease), LockReleaseRequestID: "holder-request"})
	if !ok || intent.Kind != tray.IntentAcceptLockRelease || intent.LockReleaseRequestID != "holder-request" {
		t.Fatalf("accept intent=%+v ok=%v", intent, ok)
	}
	vm.LockReleaseRequests[0].Role = "requester"
	if _, ok := translateAction(vm, ActionRequest{Kind: string(tray.IntentDismissLockRelease), LockReleaseRequestID: "holder-request"}); ok {
		t.Fatal("requester projection was allowed to answer holder request")
	}
}
