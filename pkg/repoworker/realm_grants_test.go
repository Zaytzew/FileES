package repoworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	"github.com/google/uuid"
)

func TestRealmGrantsCanonicalProjectionDirectoryAndRebuild(t *testing.T) {
	root := t.TempDir()
	authz := filepath.Join(t.TempDir(), "data.authz")
	runner := &publishRunner{}
	p := ServicePublisher{ServiceWC: root, DataAuthzFile: authz, Runner: runner, Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }}
	ownerRealm, recipientRealm, otherRealm := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ownerClient, recipientClient, otherClient := uuid.NewString(), uuid.NewString(), uuid.NewString()
	writeCanonicalRealmClient := func(realmID, alias, visibility, clientID string) {
		realm := realmRecord{Schema: "filees.realm/v1", RealmID: realmID, State: "active", CreatedAt: time.Now().UTC(), Alias: alias, DirectoryVisibility: visibility}
		if err := atomicJSON(filepath.Join(root, "admin", "realms", realmID+".json"), realm); err != nil {
			t.Fatal(err)
		}
		client := map[string]any{"schema": "filees.client-instance/v1", "client_id": clientID, "realm_id": realmID, "state": "active"}
		if err := atomicJSON(filepath.Join(root, "admin", "clients", clientID+".json"), client); err != nil {
			t.Fatal(err)
		}
		view := clientview.View{Schema: clientview.Schema, ClientID: clientID, RealmID: realmID, Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}
		if _, err := clientview.StoreIfNewer(filepath.Join(root, "clients", clientID, "view.json"), view); err != nil {
			t.Fatal(err)
		}
	}
	writeCanonicalRealmClient(ownerRealm, "owner", "hidden", ownerClient)
	writeCanonicalRealmClient(recipientRealm, "recipient", "listed", recipientClient)
	writeCanonicalRealmClient(otherRealm, "other", "hidden", otherClient)
	repoID := uuid.NewString()
	url := "svn+ssh://_filees-client@example/repos/" + repoID
	if err := p.Publish(context.Background(), repoID, ownerRealm, "Docs", url); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate(context.Background(), repoID, ownerRealm); err != nil {
		t.Fatal(err)
	}

	recipients, err := p.ListGrantRecipients(context.Background(), ownerRealm)
	if err != nil || len(recipients) != 1 || recipients[0].RealmID != recipientRealm {
		t.Fatalf("recipients=%+v err=%v", recipients, err)
	}
	if _, err := p.SetRealmDirectoryVisibility(context.Background(), otherRealm, "listed"); err != nil {
		t.Fatal(err)
	}
	recipients, _ = p.ListGrantRecipients(context.Background(), ownerRealm)
	if len(recipients) != 2 {
		t.Fatalf("listed recipients=%+v", recipients)
	}

	record, err := p.Grant(context.Background(), ownerRealm, recipientRealm, repoID, "r")
	if err != nil || record.State != "active" || record.Access != "r" || record.PathOwnerPolicy != "" {
		t.Fatalf("grant=%+v err=%v", record, err)
	}
	view, _ := clientview.Load(filepath.Join(root, "clients", recipientClient, "view.json"))
	if len(view.Repositories) != 1 || view.Repositories[0].Access != "r" || view.RealmAlias != "recipient" {
		t.Fatalf("recipient view=%+v", view.Repositories)
	}
	raw, _ := os.ReadFile(authz)
	if !strings.Contains(string(raw), "reader-"+repoID+" = "+recipientClient) {
		t.Fatalf("reader authz=%s", raw)
	}

	record, err = p.Grant(context.Background(), ownerRealm, recipientRealm, repoID, "rw")
	if err != nil || record.PathOwnerPolicy != "first_committer" {
		t.Fatalf("rw grant=%+v err=%v", record, err)
	}
	raw, _ = os.ReadFile(authz)
	if !strings.Contains(string(raw), "writer-"+repoID+" = "+recipientClient) {
		t.Fatalf("writer authz=%s", raw)
	}

	// A later installation inherits the realm grant from canonical records.
	secondClient := uuid.NewString()
	writeCanonicalRealmClient(recipientRealm, "recipient", "listed", secondClient)
	if err := p.RebuildGrantAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, _ := clientview.Load(filepath.Join(root, "clients", secondClient, "view.json"))
	if len(second.Repositories) != 1 || second.Repositories[0].Access != "rw" || second.RealmAlias != "recipient" {
		t.Fatalf("new client view=%+v alias=%q", second.Repositories, second.RealmAlias)
	}

	// Revoking one installation does not change the realm grant.
	var client map[string]any
	clientPath := filepath.Join(root, "admin", "clients", recipientClient+".json")
	clientRaw, _ := os.ReadFile(clientPath)
	_ = json.Unmarshal(clientRaw, &client)
	client["state"] = "revoked"
	if err := atomicJSON(clientPath, client); err != nil {
		t.Fatal(err)
	}
	if err := p.RebuildGrantAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(authz)
	if strings.Contains(string(raw), recipientClient) || !strings.Contains(string(raw), secondClient) {
		t.Fatalf("client revoke changed realm grant authz=%s", raw)
	}

	// A grantee, even with rw, cannot redelegate a repository it does not own.
	if _, err := p.Grant(context.Background(), recipientRealm, otherRealm, repoID, "r"); err == nil {
		t.Fatal("transitive delegation was accepted")
	}

	revoked, err := p.RevokeGrant(context.Background(), ownerRealm, recipientRealm, repoID)
	if err != nil || revoked.State != "revoked" {
		t.Fatalf("revoke=%+v err=%v", revoked, err)
	}
	second, _ = clientview.Load(filepath.Join(root, "clients", secondClient, "view.json"))
	if len(second.Repositories) != 0 {
		t.Fatalf("revoked grant remains projected: %+v", second.Repositories)
	}
	if owner, _ := clientview.Load(filepath.Join(root, "clients", ownerClient, "view.json")); len(owner.Repositories) != 1 {
		t.Fatalf("owner lost repository: %+v", owner.Repositories)
	}

	// Authz is a disposable projection: corrupt it and rebuild from canonical
	// repositories, grants and active client records only.
	if err := os.WriteFile(authz, []byte("forged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.RebuildGrantAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(authz)
	if strings.Contains(string(raw), "forged") || strings.Contains(string(raw), secondClient) || !strings.Contains(string(raw), ownerClient) {
		t.Fatalf("rebuilt authz=%s", raw)
	}
}

func TestRealmDirectoryListingRequiresExplicitAliasAndVisibility(t *testing.T) {
	root := t.TempDir()
	runner := &publishRunner{}
	p := ServicePublisher{ServiceWC: root, Runner: runner}
	realmID := uuid.NewString()
	if err := atomicJSON(filepath.Join(root, "admin", "realms", realmID+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: realmID, State: "active", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.SetRealmDirectoryVisibility(context.Background(), realmID, "listed"); err == nil {
		t.Fatal("anonymous realm became listed")
	}
	if visibility, err := p.SetRealmDirectoryVisibility(context.Background(), realmID, "hidden"); err != nil || visibility != "hidden" {
		t.Fatalf("hidden=%q err=%v", visibility, err)
	}
}
