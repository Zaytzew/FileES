package servertool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	calls         int
	realm, reason string
}

func (r *fakeRealmRevoker) RevokeRealm(_ context.Context, realm, reason string) ([]string, error) {
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
