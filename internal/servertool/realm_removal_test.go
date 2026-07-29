package servertool

import (
	"context"
	"strings"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

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
	if _, err := coordinator.Confirm(context.Background(), repoworker.Session{ClientID: "other", RealmID: uuid.NewString()}, record.OperationID, "CODE"); err == nil {
		t.Fatal("different realm confirmed operation")
	}
}
