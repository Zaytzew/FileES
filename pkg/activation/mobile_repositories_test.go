package activation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/onboarding"

	"github.com/google/uuid"
)

// writeCanonicalRepositoryRecord seeds admin/repositories/<repoID>.json
// exactly as pkg/repoworker.ServicePublisher.Publish would - this test
// writes it directly (no svn add/commit) since mobileRepositoryEntries only
// ever reads the local filesystem, never SVN state.
func writeCanonicalRepositoryRecord(t *testing.T, wc, repoID, realmID, state string) {
	t.Helper()
	path := filepath.Join(wc, "admin", "repositories", repoID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"schema": canonicalRepositorySchema, "repo_id": repoID, "owner_realm_id": realmID,
		"display_name": "Dokumenty", "url": "svn+ssh://_filees-data@example.net/" + repoID,
		"state": state, "created_at": time.Now().UTC(),
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testMobileActivationGrant(t *testing.T, expires time.Time, repos []onboarding.MobileRepositoryGrant) onboarding.ActivationGrant {
	t.Helper()
	grant := testActivationGrant(t, expires)
	grant.Kind = onboarding.KindMobile
	grant.Repositories = repos
	return grant
}

// TestPublishSeedsMobileViewWithInheritedRepositories confirms a paired
// mobile client's view.json is seeded from the pairing initiator's own
// repository grants (Access/AttachmentPolicy carried over verbatim,
// DisplayName/URL/OwnerRealmID/State read fresh from the canonical
// repository record) instead of the "repositories": [] every client got
// before this feature existed.
func TestPublishSeedsMobileViewWithInheritedRepositories(t *testing.T) {
	manager, config := newActivationTestManager(t)
	config.MobileEntryPath = "/usr/local/libexec/filees/filees-mobile-v1"
	manager, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	grant := testMobileActivationGrant(t, time.Now().Add(time.Hour), []onboarding.MobileRepositoryGrant{
		{RepoID: repoID, Access: "r", AttachmentPolicy: "required"},
	})
	writeCanonicalRepositoryRecord(t, config.ServiceWorkingCopy, repoID, grant.RealmID, "active")

	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}

	view, err := clientview.Load(filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Repositories) != 1 {
		t.Fatalf("view.repositories=%+v, want exactly 1 inherited entry", view.Repositories)
	}
	got := view.Repositories[0]
	if got.RepoID != repoID || got.Access != "r" || got.AttachmentPolicy != "required" || got.State != "active" || got.DisplayName != "Dokumenty" || got.OwnerRealmID != grant.RealmID {
		t.Fatalf("inherited repository entry=%+v", got)
	}
}

// TestPublishReadsMobileRepositoryStateFreshNotFromMintTimeSnapshot: a
// repository's canonical State can move initializing -> active during the
// (up to 5 minute) pairing TTL window between minting the token and the
// phone finishing activation. The inherited view must reflect the state at
// Publish time, not whatever it was when the grant was minted.
func TestPublishReadsMobileRepositoryStateFreshNotFromMintTimeSnapshot(t *testing.T) {
	manager, config := newActivationTestManager(t)
	config.MobileEntryPath = "/usr/local/libexec/filees/filees-mobile-v1"
	manager, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	grant := testMobileActivationGrant(t, time.Now().Add(time.Hour), []onboarding.MobileRepositoryGrant{
		{RepoID: repoID, Access: "rw", AttachmentPolicy: "optional"},
	})
	// State at "mint time" is still initializing...
	writeCanonicalRepositoryRecord(t, config.ServiceWorkingCopy, repoID, grant.RealmID, "initializing")
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	// ...but flips to active before the phone finishes, same as a real
	// concurrent INITIAL_COMMIT completing during the pairing window.
	writeCanonicalRepositoryRecord(t, config.ServiceWorkingCopy, repoID, grant.RealmID, "active")

	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	view, err := clientview.Load(filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Repositories) != 1 || view.Repositories[0].State != "active" {
		t.Fatalf("view.repositories=%+v, want state read fresh as active", view.Repositories)
	}
}

// TestPublishSkipsMobileRepositoryWithNoCanonicalRecord: a repository that
// vanished (deleted/renamed) between mint and finish must not block the
// whole activation - the entry is silently skipped rather than the
// operation failing outright.
func TestPublishSkipsMobileRepositoryWithNoCanonicalRecord(t *testing.T) {
	manager, config := newActivationTestManager(t)
	config.MobileEntryPath = "/usr/local/libexec/filees/filees-mobile-v1"
	manager, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	missingRepoID := uuid.NewString()
	grant := testMobileActivationGrant(t, time.Now().Add(time.Hour), []onboarding.MobileRepositoryGrant{
		{RepoID: missingRepoID, Access: "rw", AttachmentPolicy: "optional"},
	})
	// Deliberately no writeCanonicalRepositoryRecord call for missingRepoID.
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatalf("activation failed outright instead of skipping the missing repository: %v", err)
	}
	view, err := clientview.Load(filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Repositories) != 0 {
		t.Fatalf("view.repositories=%+v, want empty (missing canonical record skipped)", view.Repositories)
	}
}

// TestPublishDesktopClientStillGetsEmptyRepositories confirms the mobile
// inheritance gate (Kind == KindMobile) does not change desktop activation
// behavior at all, even if a Repositories list were somehow present on a
// desktop grant.
func TestPublishDesktopClientStillGetsEmptyRepositories(t *testing.T) {
	manager, config := newActivationTestManager(t)
	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	grant.Repositories = []onboarding.MobileRepositoryGrant{{RepoID: uuid.NewString(), Access: "rw"}}
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	view, err := clientview.Load(filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Repositories) != 0 {
		t.Fatalf("desktop view.repositories=%+v, want empty regardless of grant.Repositories", view.Repositories)
	}
}
