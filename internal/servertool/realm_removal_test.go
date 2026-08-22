package servertool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/activation"
	control "filees/pkg/control/v1"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

type fakeRealmDeleteBackend struct {
	calls    []string
	failOnce bool
}

func (b *fakeRealmDeleteBackend) Delete(_ context.Context, operationID, realmID, repoID string) (time.Time, error) {
	b.calls = append(b.calls, operationID+":"+realmID+":"+repoID)
	if b.failOnce && len(b.calls) == 2 {
		return time.Time{}, errors.New("interrupted archive")
	}
	return time.Now(), nil
}

type fakeRealmGrantPublisher struct {
	calls int
	realm string
	repos []string
}

type fakeRealmRecoveryPublisher struct {
	calls int
	fail  error
}

func (p *fakeRealmRecoveryPublisher) Prepare(_ repoworker.RealmRemovalRecord) error {
	p.calls++
	return p.fail
}

func (p *fakeRealmGrantPublisher) WithdrawRealmGrants(_ context.Context, realm string, repos []string) error {
	p.calls++
	p.realm, p.repos = realm, append([]string(nil), repos...)
	return nil
}

type fakeRealmRevoker struct {
	fenceCalls    int
	calls         int
	realm, reason string
	failFence     bool
}

type fakeRealmFenceReader struct {
	fence activation.RealmRemovalFence
	found bool
	err   error
}

func (r fakeRealmFenceReader) RealmRemovalFenceForRealm(string) (activation.RealmRemovalFence, bool, error) {
	return r.fence, r.found, r.err
}

func (r *fakeRealmRevoker) FenceRealmRemoval(_ string, _ string, _ []string) error {
	r.fenceCalls++
	if r.failFence {
		return errors.New("credential snapshot changed")
	}
	return nil
}

func (r *fakeRealmRevoker) RevokeRealmRemoval(_ context.Context, realm, _ string, _ []string, reason string) ([]string, error) {
	r.calls++
	r.realm, r.reason = realm, reason
	return nil, nil
}

func TestRealmRemovalCoordinatorDerivesScopeAndBindsRealm(t *testing.T) {
	realm, client, owned, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := repoworker.RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 3}
	coordinator := realmRemovalCoordinator{
		Store: store,
		SnapshotScope: func(gotRealm string) (repoworker.RealmRemovalScope, error) {
			if gotRealm != realm {
				t.Fatalf("snapshot realm=%q want %q", gotRealm, realm)
			}
			return repoworker.RealmRemovalScope{OwnedRepoIDs: []string{owned}, ForeignGrantRepoIDs: []string{foreign}}, nil
		},
		ActiveClients: func(gotRealm string) ([]string, error) {
			if gotRealm != realm {
				t.Fatalf("client realm=%q want %q", gotRealm, realm)
			}
			return []string{client}, nil
		},
	}
	session := repoworker.Session{ClientID: "client-a", RealmID: realm}
	record, err := coordinator.Request(context.Background(), session, uuid.NewString(), repoworker.RealmRemovalRequest{NotificationEmail: "user@example.net", ErasureRequested: true})
	if err != nil || record.RealmID != realm || len(record.Scope.ClientIDs) != 1 || len(record.Scope.OwnedRepoIDs) != 1 || len(record.Scope.ForeignGrantRepoIDs) != 1 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if _, _, err := coordinator.Confirm(context.Background(), repoworker.Session{ClientID: "other", RealmID: uuid.NewString()}, record.OperationID, "CODE"); err == nil {
		t.Fatal("different realm confirmed operation")
	}
}

func TestRealmRemovalAdmissionBlocksPostCrashMutations(t *testing.T) {
	realmID, operationID := uuid.NewString(), uuid.NewString()
	session := repoworker.Session{ClientID: uuid.NewString(), RealmID: realmID}
	create := control.Ticket{Type: control.TicketCreateRepository, OperationID: uuid.NewString()}
	if err := (realmRemovalAdmission{Fences: fakeRealmFenceReader{}}).Admit(session, create); err != nil {
		t.Fatalf("unfenced realm was blocked: %v", err)
	}
	if err := (realmRemovalAdmission{Fences: fakeRealmFenceReader{err: errors.New("corrupt fence")}}).Admit(session, create); err == nil {
		t.Fatal("corrupt fence state failed open")
	}
	admission := realmRemovalAdmission{Fences: fakeRealmFenceReader{
		fence: activation.RealmRemovalFence{OperationID: operationID, RealmID: realmID}, found: true,
	}}
	for _, typ := range []control.TicketType{
		control.TicketCreateRepository, control.TicketInitialCommit, control.TicketDeleteRepository,
		control.TicketMobilePairing, control.TicketClaimRealmAlias, control.TicketClientDeactivate,
		control.TicketRealmRemoveRequest, control.TicketLoadRepositoryDump, control.TicketStoragePreflight,
		control.TicketGrantAccess, control.TicketRevokeAccess, control.TicketSetRealmVisibility,
		control.TicketGetRealmPublicBranding, control.TicketSetRealmPublicBranding,
		control.TicketListGrantRecipients, control.TicketCreatePublicShare, control.TicketUpdatePublicShare,
		control.TicketRevokePublicShare, control.TicketDeletePublicShare,
		control.TicketListUploadChannels, control.TicketCreateUploadChannel, control.TicketUpdateUploadChannel,
		control.TicketRevokeUploadChannel, control.TicketDeleteUploadChannel,
	} {
		if err := admission.Admit(session, control.Ticket{Type: typ, OperationID: uuid.NewString()}); err == nil {
			t.Fatalf("fenced realm admitted %s", typ)
		}
	}
	if err := admission.Admit(session, control.Ticket{Type: control.TicketRealmRemoveConfirm, OperationID: uuid.NewString()}); err == nil {
		t.Fatal("fenced realm resumed a different removal operation")
	}
	if err := admission.Admit(session, control.Ticket{Type: control.TicketRealmRemoveConfirm, OperationID: operationID}); err != nil {
		t.Fatalf("matching removal confirmation was blocked: %v", err)
	}
	if err := admission.Admit(session, control.Ticket{Type: control.TicketResolveOwnerLabels, OperationID: uuid.NewString()}); err != nil {
		t.Fatalf("read-only owner-label resolution was blocked: %v", err)
	}
}

func TestRealmRemovalCoordinatorRejectsScopeDriftBeforeOTPBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*[]string, *[]string, *[]string)
	}{
		{"client", func(clients, _, _ *[]string) { *clients = append(*clients, uuid.NewString()) }},
		{"owned repository", func(_, owned, _ *[]string) { *owned = append(*owned, uuid.NewString()) }},
		{"foreign grant", func(_, _, foreign *[]string) { *foreign = append(*foreign, uuid.NewString()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			realm := uuid.NewString()
			clients := []string{uuid.NewString(), uuid.NewString()}
			owned := []string{uuid.NewString()}
			foreign := []string{uuid.NewString()}
			store := repoworker.RealmRemovalStore{
				Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)),
				TTL: time.Hour, Attempts: 3,
			}
			executed := false
			coordinator := realmRemovalCoordinator{
				Store: store,
				SnapshotScope: func(string) (repoworker.RealmRemovalScope, error) {
					return repoworker.RealmRemovalScope{
						OwnedRepoIDs: append([]string(nil), owned...), ForeignGrantRepoIDs: append([]string(nil), foreign...),
					}, nil
				},
				ActiveClients: func(string) ([]string, error) {
					return append([]string(nil), clients...), nil
				},
				Execute: func(context.Context, repoworker.RealmRemovalRecord) error {
					executed = true
					return nil
				},
			}
			session := repoworker.Session{ClientID: clients[0], RealmID: realm}
			record, err := coordinator.Request(context.Background(), session, uuid.NewString(), repoworker.RealmRemovalRequest{
				NotificationEmail: "user@example.net", RecoveryPublicKey: testRecoveryPublicKey(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			job, err := store.ClaimPendingMail(time.Minute)
			if err != nil {
				t.Fatal(err)
			}

			// A target added after the user saw the destructive counts must
			// never silently expand the exact snapshot authorized by that OTP.
			tc.mutate(&clients, &owned, &foreign)
			if _, _, err := coordinator.Confirm(context.Background(), session, record.OperationID, job.OTP); err == nil {
				t.Fatal("scope drift was accepted at the OTP boundary")
			}
			if executed {
				t.Fatal("scope drift reached destructive executor")
			}
			current, err := store.Load(record.OperationID)
			if err != nil || current.State != repoworker.RealmRemovalAwaitingConfirmation {
				t.Fatalf("drifted operation state=%q err=%v", current.State, err)
			}
		})
	}
}

func TestRealmRemovalExecutorResumesAfterInterruptedArchive(t *testing.T) {
	realm, repo1, repo2, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := repoworker.RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 3}
	record, otp, err := store.Begin(realm, repoworker.RealmRemovalScope{OwnedRepoIDs: []string{repo1, repo2}, ForeignGrantRepoIDs: []string{foreign}}, repoworker.RealmRemovalRequest{NotificationEmail: "user@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	record, err = store.Confirm(record.OperationID, otp)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeRealmDeleteBackend{failOnce: true}
	recovery := &fakeRealmRecoveryPublisher{}
	grants, revoker := &fakeRealmGrantPublisher{}, &fakeRealmRevoker{}
	executor := realmRemovalExecutor{Store: store, Backend: backend, Recovery: recovery, Publisher: grants, Activation: revoker}
	if err := executor.Execute(context.Background(), record); err == nil {
		t.Fatal("interrupted archive completed")
	}
	partial, err := store.Load(record.OperationID)
	if err != nil || partial.State != repoworker.RealmRemovalDeleting || recovery.calls != 0 || grants.calls != 0 || revoker.calls != 0 {
		t.Fatalf("partial=%+v recovery=%d grants=%d revoker=%d err=%v", partial, recovery.calls, grants.calls, revoker.calls, err)
	}
	if err := executor.Execute(context.Background(), partial); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Load(record.OperationID)
	if err != nil || completed.State != repoworker.RealmRemovalCompleted || recovery.calls != 1 || grants.calls != 1 || revoker.calls != 1 || grants.realm != realm || revoker.realm != realm {
		t.Fatalf("completed=%+v recovery=%d grants=%+v revoker=%+v err=%v", completed, recovery.calls, grants, revoker, err)
	}
	if len(grants.repos) != 1 || grants.repos[0] != foreign || !strings.Contains(revoker.reason, "confirmed") {
		t.Fatalf("grants=%+v revoker=%+v", grants, revoker)
	}
}

func TestRealmRemovalExecutorDoesNotRevokeBeforeRecoveryIsPublished(t *testing.T) {
	realm, repo := uuid.NewString(), uuid.NewString()
	store := repoworker.RealmRemovalStore{Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 3}
	record, otp, err := store.Begin(realm, repoworker.RealmRemovalScope{OwnedRepoIDs: []string{repo}}, repoworker.RealmRemovalRequest{NotificationEmail: "user@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	record, err = store.Confirm(record.OperationID, otp)
	if err != nil {
		t.Fatal(err)
	}
	recovery := &fakeRealmRecoveryPublisher{fail: errors.New("manifest unavailable")}
	grants, revoker := &fakeRealmGrantPublisher{}, &fakeRealmRevoker{}
	executor := realmRemovalExecutor{Store: store, Backend: &fakeRealmDeleteBackend{}, Recovery: recovery, Publisher: grants, Activation: revoker}
	if err := executor.Execute(context.Background(), record); err == nil {
		t.Fatal("recovery publication failure did not stop removal")
	}
	current, err := store.Load(record.OperationID)
	if err != nil || current.State != repoworker.RealmRemovalDeleting || recovery.calls != 1 || grants.calls != 0 || revoker.calls != 0 {
		t.Fatalf("current=%+v recovery=%d grants=%d revoker=%d err=%v", current, recovery.calls, grants.calls, revoker.calls, err)
	}
}

func TestRealmRemovalExecutorFencesCredentialsBeforeDeletingRepositories(t *testing.T) {
	realm, repo, client := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := repoworker.RealmRemovalStore{
		Root: t.TempDir(), OTPPepper: []byte(strings.Repeat("p", 32)), TTL: time.Hour, Attempts: 3,
	}
	record, otp, err := store.Begin(realm, repoworker.RealmRemovalScope{
		ClientIDs: []string{client}, OwnedRepoIDs: []string{repo},
	}, repoworker.RealmRemovalRequest{NotificationEmail: "user@example.net"})
	if err != nil {
		t.Fatal(err)
	}
	record, err = store.Confirm(record.OperationID, otp)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeRealmDeleteBackend{}
	revoker := &fakeRealmRevoker{failFence: true}
	executor := realmRemovalExecutor{
		Store: store, Backend: backend, Recovery: &fakeRealmRecoveryPublisher{},
		Publisher: &fakeRealmGrantPublisher{}, Activation: revoker,
	}
	if err := executor.Execute(context.Background(), record); err == nil {
		t.Fatal("credential fence failure did not stop realm removal")
	}
	if revoker.fenceCalls != 1 || len(backend.calls) != 0 {
		t.Fatalf("fence calls=%d delete calls=%v", revoker.fenceCalls, backend.calls)
	}
	current, err := store.Load(record.OperationID)
	if err != nil || current.State != repoworker.RealmRemovalDeleting {
		t.Fatalf("state after fence failure=%q err=%v", current.State, err)
	}
}

func TestRealmRemovalExecutorPersistsErasureOnlyAfterOTPAndActiveDeletion(t *testing.T) {
	realm, repo := uuid.NewString(), uuid.NewString()
	root := t.TempDir()
	store := repoworker.RealmRemovalStore{
		Root: filepath.Join(root, "realm-removals"), OTPPepper: []byte(strings.Repeat("p", 32)),
		TTL: time.Hour, Attempts: 3,
	}
	record, otp, err := store.Begin(realm, repoworker.RealmRemovalScope{OwnedRepoIDs: []string{repo}}, repoworker.RealmRemovalRequest{
		NotificationEmail: "user@example.net", ErasureRequested: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	erasure := repoworker.DataErasureStore{Root: filepath.Join(root, "data-erasure")}
	if _, err := erasure.Load(record.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("erasure journal existed before OTP: %v", err)
	}
	record, err = store.Confirm(record.OperationID, otp)
	if err != nil {
		t.Fatal(err)
	}
	executor := realmRemovalExecutor{
		Store: store, Backend: &fakeRealmDeleteBackend{}, Recovery: &fakeRealmRecoveryPublisher{},
		Publisher: &fakeRealmGrantPublisher{}, Activation: &fakeRealmRevoker{},
		Erasure: erasure, ErasureMaxDays: 90,
	}
	if err := executor.Execute(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	request, err := erasure.Load(record.OperationID)
	if err != nil || request.State != repoworker.DataErasureAwaitingBackupRetention ||
		request.ActiveDataDeletedAt == nil || !request.CompletionDueAt.Equal(record.ConfirmedAt.AddDate(0, 0, 90)) {
		t.Fatalf("unexpected erasure request: %+v err=%v", request, err)
	}
}
