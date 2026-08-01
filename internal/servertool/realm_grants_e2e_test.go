package servertool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

// TestRealmGrantsDestructiveE2E shares the real-SVN fixture with realm
// removal. It drives signed-ticket worker semantics, canonical grant records,
// view/authz rebuilds and the activation manager rather than hand-editing a
// projection. The fixture remains isolated under t.TempDir on Linux/OpenBSD.
func TestRealmGrantsDestructiveE2E(t *testing.T) {
	f := newRealmRemovalE2EFixture(t, 0)
	store, err := repoworker.NewFileStore(filepath.Join(f.root, "grant-results"))
	if err != nil {
		t.Fatal(err)
	}
	worker := &repoworker.Worker{Store: store, Grants: f.publisher}
	ownerSession := repoworker.Session{ClientID: f.targetClients[0].ClientID, RealmID: f.targetRealm, CanCreateRepositories: true}
	recipientSession := repoworker.Session{ClientID: f.otherClient.ClientID, RealmID: f.otherRealm, CanCreateRepositories: true}

	issue := func(session repoworker.Session, typ control.TicketType, payload any) control.Result {
		t.Helper()
		ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, session.ClientID, payload, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		result, err := worker.Handle(context.Background(), session, ticket)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	readRepo, writeRepo := f.ownedRepos[0], f.ownedRepos[1]
	if result := issue(ownerSession, control.TicketGrantAccess, control.GrantAccessPayload{RepoID: readRepo.RepoID, RecipientRealmID: f.otherRealm, Access: "r"}); result.Status != control.ResultOK {
		t.Fatalf("read grant=%+v", result)
	}
	if result := issue(ownerSession, control.TicketGrantAccess, control.GrantAccessPayload{RepoID: writeRepo.RepoID, RecipientRealmID: f.otherRealm, Access: "rw"}); result.Status != control.ResultOK {
		t.Fatalf("write grant=%+v", result)
	}
	view, err := clientview.Load(filepath.Join(f.activationConfig.ServiceWorkingCopy, "clients", f.otherClient.ClientID, "view.json"))
	if err != nil || repositoryAccess(view, readRepo.RepoID) != "r" || repositoryAccess(view, writeRepo.RepoID) != "rw" {
		t.Fatalf("recipient view=%+v err=%v", view.Repositories, err)
	}

	// A later desktop installation inherits both realm grants without any ACL
	// decision tied to its fresh client_id/key.
	later := f.activate(t, f.otherRealm)
	recipientSession.ClientID = later.ClientID
	laterView, err := clientview.Load(filepath.Join(f.activationConfig.ServiceWorkingCopy, "clients", later.ClientID, "view.json"))
	if err != nil || repositoryAccess(laterView, readRepo.RepoID) != "r" || repositoryAccess(laterView, writeRepo.RepoID) != "rw" {
		t.Fatalf("later view=%+v err=%v", laterView.Repositories, err)
	}

	// Revoking one installation removes only that transport identity. The
	// canonical realm grant and access of the later installation survive.
	if _, err := f.manager.Revoke(context.Background(), f.otherClient.ClientID, "realm grant e2e client revoke"); err != nil {
		t.Fatal(err)
	}
	authz, _ := os.ReadFile(f.activationConfig.DataAuthzFile)
	if strings.Contains(string(authz), f.otherClient.ClientID) || !strings.Contains(string(authz), later.ClientID) {
		t.Fatalf("post-client-revoke authz=%s", authz)
	}

	// Received rw access never conveys delegation. The worker derives the
	// alleged grantor realm from the authenticated recipient session.
	if result := issue(recipientSession, control.TicketGrantAccess, control.GrantAccessPayload{RepoID: writeRepo.RepoID, RecipientRealmID: uuid.NewString(), Access: "r"}); result.Status != control.ResultError {
		t.Fatalf("transitive grant=%+v", result)
	}

	localSentinel := filepath.Join(f.root, "detached-local-data", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(localSentinel), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localSentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := issue(ownerSession, control.TicketRevokeAccess, control.RevokeAccessPayload{RepoID: writeRepo.RepoID, RecipientRealmID: f.otherRealm}); result.Status != control.ResultOK {
		t.Fatalf("grant revoke=%+v", result)
	}
	laterView, _ = clientview.Load(filepath.Join(f.activationConfig.ServiceWorkingCopy, "clients", later.ClientID, "view.json"))
	if repositoryAccess(laterView, writeRepo.RepoID) != "" {
		t.Fatalf("revoked repo remains projected: %+v", laterView.Repositories)
	}
	if raw, err := os.ReadFile(localSentinel); err != nil || string(raw) != "keep" {
		t.Fatalf("local data changed: %q err=%v", raw, err)
	}

	// The projection is disposable and can be reconstructed after corruption.
	if err := os.WriteFile(f.activationConfig.DataAuthzFile, []byte("forged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.publisher.RebuildGrantAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	authz, _ = os.ReadFile(f.activationConfig.DataAuthzFile)
	if strings.Contains(string(authz), "forged") || !strings.Contains(string(authz), later.ClientID) {
		t.Fatalf("rebuilt authz=%s", authz)
	}
}

func repositoryAccess(view clientview.View, repoID string) string {
	for _, repository := range view.Repositories {
		if repository.RepoID == repoID {
			return repository.Access
		}
	}
	return ""
}
