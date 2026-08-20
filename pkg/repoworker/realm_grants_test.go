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
	"filees/pkg/realmbranding"
	"github.com/google/uuid"
)

func TestRealmPublicBrandingIsStoredOnceOnRealm(t *testing.T) {
	root := t.TempDir()
	realmID := uuid.NewString()
	if err := atomicJSON(filepath.Join(root, "admin", "realms", realmID+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: realmID, State: "active", CreatedAt: time.Now().UTC(), Alias: "acme"}); err != nil {
		t.Fatal(err)
	}
	runner := &publishRunner{}
	publisher := ServicePublisher{ServiceWC: root, Runner: runner}
	want := realmbranding.Branding{LeadingColor: "#008C45"}
	got, err := publisher.SetRealmPublicBranding(context.Background(), realmID, want)
	if err != nil || got != want || runner.calls != 1 {
		t.Fatalf("SetRealmPublicBranding()=%+v calls=%d err=%v", got, runner.calls, err)
	}
	got, err = publisher.RealmPublicBranding(context.Background(), realmID)
	if err != nil || got != want {
		t.Fatalf("RealmPublicBranding()=%+v err=%v", got, err)
	}
	if _, err := publisher.SetRealmPublicBranding(context.Background(), realmID, want); err != nil || runner.calls != 1 {
		t.Fatalf("idempotent set calls=%d err=%v", runner.calls, err)
	}
}

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

	recipients, err := p.ListGrantRecipients(context.Background(), ownerRealm, "")
	if err != nil || len(recipients) != 1 || recipients[0].RealmID != recipientRealm {
		t.Fatalf("recipients=%+v err=%v", recipients, err)
	}
	if _, err := p.SetRealmDirectoryVisibility(context.Background(), otherRealm, "listed"); err != nil {
		t.Fatal(err)
	}
	recipients, _ = p.ListGrantRecipients(context.Background(), ownerRealm, "")
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
	if !strings.Contains(string(raw), "["+repoID+":/.filees-whales]\n* =") {
		t.Fatalf("Whale namespace is not denied by canonical authz=%s", raw)
	}

	record, err = p.Grant(context.Background(), ownerRealm, recipientRealm, repoID, "rw")
	if err != nil || record.PathOwnerPolicy != "first_committer" {
		t.Fatalf("rw grant=%+v err=%v", record, err)
	}
	raw, _ = os.ReadFile(authz)
	if !strings.Contains(string(raw), "writer-"+repoID+" = "+recipientClient) {
		t.Fatalf("writer authz=%s", raw)
	}
	if _, err := p.SetRealmDirectoryVisibility(context.Background(), recipientRealm, "hidden"); err != nil {
		t.Fatal(err)
	}
	recipients, err = p.ListGrantRecipients(context.Background(), ownerRealm, repoID)
	if err != nil || len(recipients) != 2 {
		t.Fatalf("repo recipients=%+v err=%v", recipients, err)
	}
	foundActiveHidden := false
	for _, recipient := range recipients {
		if recipient.RealmID == recipientRealm {
			foundActiveHidden = recipient.State == "active" && recipient.Access == "rw"
		}
	}
	if !foundActiveHidden {
		t.Fatalf("hidden active grant missing from management list: %+v", recipients)
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

// The editing policy is a property of the repository, not of a client's
// grant, so projectedRepositories must always read it from the canonical
// record. AttachmentPolicy and MetadataDigest deliberately do the opposite -
// they are per-client and are carried over from the previous projection -
// and mixing the two rules is the exact mistake that would let a stale view
// pin a repository to a policy its owner already changed.
func TestProjectedRepositoriesSourcesEditingPolicyFromRecordNotPreviousView(t *testing.T) {
	realmID := uuid.NewString()
	repoID := uuid.NewString()
	records := map[string]repositoryRecord{repoID: {
		Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: realmID,
		DisplayName: "Docs", URL: "svn+ssh://_filees-client@example.net/" + repoID,
		State: "active", EditingPolicy: clientview.EditingLockRequired,
	}}
	// The previous projection disagrees on every carried field: it claims the
	// repository is free and pins a non-default attachment policy.
	previous := []clientview.Repository{{
		RepoID: repoID, DisplayName: "Docs", URL: records[repoID].URL, Access: "rw",
		State: "active", OwnerRealmID: realmID,
		AttachmentPolicy: "required", MetadataDigest: "sha256:stale",
		EditingPolicy: clientview.EditingFree,
	}}

	got := projectedRepositories(records, nil, realmID, "normal", "desktop", previous)
	if len(got) != 1 {
		t.Fatalf("projected %d repositories, want 1", len(got))
	}
	if got[0].EditingPolicy != clientview.EditingLockRequired {
		t.Fatalf("editing_policy=%q, want the record's %q (stale view must not win)", got[0].EditingPolicy, clientview.EditingLockRequired)
	}
	if !got[0].RequiresLock() {
		t.Fatal("RequiresLock() disagrees with the projected policy")
	}
	// Guard the contrast: these two must still be inherited, or this test
	// would pass for the wrong reason after a careless refactor.
	if got[0].AttachmentPolicy != "required" || got[0].MetadataDigest != "sha256:stale" {
		t.Fatalf("per-client fields lost their carry-over: attachment=%q digest=%q", got[0].AttachmentPolicy, got[0].MetadataDigest)
	}
}

// Turning the policy off must actually reach clients. Because the default is
// the empty string, a regression that "preserves" the old value instead of
// reading the record would leave every client locked forever with no way back.
func TestProjectedRepositoriesClearsEditingPolicyWhenRecordReturnsToFree(t *testing.T) {
	realmID := uuid.NewString()
	repoID := uuid.NewString()
	records := map[string]repositoryRecord{repoID: {
		Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: realmID,
		DisplayName: "Docs", URL: "svn+ssh://_filees-client@example.net/" + repoID,
		State: "active", EditingPolicy: clientview.EditingFree,
	}}
	previous := []clientview.Repository{{
		RepoID: repoID, DisplayName: "Docs", URL: records[repoID].URL, Access: "rw",
		State: "active", OwnerRealmID: realmID, EditingPolicy: clientview.EditingLockRequired,
	}}

	got := projectedRepositories(records, nil, realmID, "normal", "desktop", previous)
	if len(got) != 1 || got[0].EditingPolicy != clientview.EditingFree {
		t.Fatalf("editing_policy=%q, want it cleared back to the default", got[0].EditingPolicy)
	}
	if got[0].RequiresLock() {
		t.Fatal("repository still requires a lock after the owner turned the policy off")
	}
}

// The editing policy governs how everyone works inside a repository, so it is
// owner-only. A recipient holding rw must not be able to impose passports on
// the owner, which is why authorization goes through OwnsActiveRepository
// rather than any grant lookup.
func TestSetRepositoryEditingPolicyIsOwnerOnlyAndNormalisesTheDefault(t *testing.T) {
	root := t.TempDir()
	runner := &publishRunner{}
	p := ServicePublisher{ServiceWC: root, DataAuthzFile: filepath.Join(root, "authz"), Runner: runner}
	owner, guest, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ownerClient := uuid.NewString()
	for _, realm := range []string{owner, guest} {
		if err := atomicJSON(filepath.Join(root, "admin", "realms", realm+".json"), realmRecord{Schema: "filees.realm/v1", RealmID: realm, State: "active", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicJSON(filepath.Join(root, "admin", "clients", ownerClient+".json"), map[string]any{"schema": "filees.client-instance/v1", "client_id": ownerClient, "realm_id": owner, "state": "active"}); err != nil {
		t.Fatal(err)
	}
	ownerView := filepath.Join(root, "clients", ownerClient, "view.json")
	if _, err := clientview.StoreIfNewer(ownerView, clientview.View{Schema: clientview.Schema, ClientID: ownerClient, RealmID: owner, Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Capabilities: &clientview.Capabilities{CanCreateRepositories: true}, Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{}}); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "admin", "repositories", repoID+".json")
	if err := atomicJSON(recordPath, repositoryRecord{
		Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: owner,
		DisplayName: "Rysunki", URL: "svn+ssh://_filees-data@example/" + repoID,
		State: "active", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// A live rw grant for the guest, so the refusal below cannot be explained
	// by the guest simply having no access at all.
	if err := atomicJSON(filepath.Join(root, "admin", "grants", repoID, guest+".json"), RealmGrantRecord{
		Schema: RealmGrantSchema, RepoID: repoID, OwnerRealmID: owner, RecipientRealmID: guest,
		Access: "rw", State: "active", PathOwnerPolicy: "first_committer",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := p.SetRepositoryEditingPolicy(context.Background(), guest, repoID, "lock_required"); err == nil {
		t.Fatal("a realm holding rw was allowed to change the repository editing policy")
	}

	got, err := p.SetRepositoryEditingPolicy(context.Background(), owner, repoID, "lock_required")
	if err != nil || got != clientview.EditingLockRequired {
		t.Fatalf("owner could not set the policy: got=%q err=%v", got, err)
	}
	stored := loadRepositoryRecordForTest(t, recordPath)
	if stored.EditingPolicy != clientview.EditingLockRequired {
		t.Fatalf("record editing_policy=%q", stored.EditingPolicy)
	}
	// Storing it is not enough: the record is the only place the projection
	// reads this from, so the owner's view has to carry it before any client
	// can act on the change.
	projected, err := clientview.Load(ownerView)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Repositories) != 1 || !projected.Repositories[0].RequiresLock() {
		t.Fatalf("policy did not reach the client projection: %+v", projected.Repositories)
	}

	// "free" is an input alias and must be stored as absence, or the default
	// would reach the wire and break every reader that predates the field.
	if got, err := p.SetRepositoryEditingPolicy(context.Background(), owner, repoID, "free"); err != nil || got != clientview.EditingFree {
		t.Fatalf("rollback to the default failed: got=%q err=%v", got, err)
	}
	stored = loadRepositoryRecordForTest(t, recordPath)
	if stored.EditingPolicy != clientview.EditingFree {
		t.Fatalf("record kept an unnormalised default: %q", stored.EditingPolicy)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "editing_policy") {
		t.Fatalf("default serialised into the canonical record: %s", raw)
	}

	if _, err := p.SetRepositoryEditingPolicy(context.Background(), owner, repoID, "readonly"); err == nil {
		t.Fatal("unknown policy accepted")
	}
}

func loadRepositoryRecordForTest(t *testing.T, path string) repositoryRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record repositoryRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
