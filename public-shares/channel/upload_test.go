package channel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

func uploadFixture(t *testing.T) (*Store, manifest.Upload, string) {
	t.Helper()
	owner, authority, uploadRepo := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store := &Store{
		Root: t.TempDir(), Authority: fakeAuthority{owner: owner, repo: authority, alias: "atmprojekt"},
		TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return now },
	}
	declaration := manifest.Upload{
		OwnerRealm: owner, AuthorityRepoID: authority, UploadRepoID: uploadRepo,
		Slug: "oferta-wykonawcy", Recipients: []string{"A@example.com"}, CollisionPolicy: manifest.CollisionDeny,
	}
	return store, declaration, owner
}

func TestCreateUploadIssuesOpaqueTokenAndSharesSlugSpace(t *testing.T) {
	store, declaration, owner := uploadFixture(t)
	channelID := uuid.NewString()
	record, deliveries, err := store.CreateUpload(channelID, owner, declaration)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StateActive || record.Manifest.UploadRepoID != declaration.UploadRepoID || record.Manifest.AuthorityRepoID != declaration.AuthorityRepoID {
		t.Fatalf("record=%+v", record)
	}
	if len(deliveries) != 1 || deliveries[0].Email != "a@example.com" || deliveries[0].Token == "" {
		t.Fatalf("deliveries=%+v", deliveries)
	}
	digest := sha256.Sum256([]byte(deliveries[0].Token))
	if record.Recipients[0].TokenHash != hex.EncodeToString(digest[:]) {
		t.Fatal("token hash mismatch")
	}
	share := manifest.Share{
		OwnerRealm: owner, RepoID: declaration.AuthorityRepoID, SourceRoot: ".", Slug: declaration.Slug,
		Recipients: []string{"b@example.com"},
		Objects:    []manifest.Object{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "plik.pdf", DisplayName: "plik.pdf"}},
	}
	if _, _, err := store.Create(uuid.NewString(), owner, share); err != ErrSlugTaken {
		t.Fatalf("shared slug was not exclusive: %v", err)
	}
}

func TestCreateUploadRejectsNonOwnerAndAnonymous(t *testing.T) {
	store, declaration, owner := uploadFixture(t)
	if _, _, err := store.CreateUpload(uuid.NewString(), uuid.NewString(), declaration); err != ErrForbidden {
		t.Fatalf("non-owner = %v", err)
	}
	declaration.Recipients = nil
	if _, _, err := store.CreateUpload(uuid.NewString(), owner, declaration); err == nil {
		t.Fatal("anonymous upload was accepted")
	}
}

func TestCreateUploadRejectsRenamePolicyAndRetainsReposOnDelete(t *testing.T) {
	store, declaration, owner := uploadFixture(t)
	declaration.CollisionPolicy = manifest.CollisionRename
	if _, _, err := store.CreateUpload(uuid.NewString(), owner, declaration); err != ErrPolicy {
		t.Fatalf("rename policy = %v", err)
	}
	declaration.CollisionPolicy = manifest.CollisionDeny
	channelID := uuid.NewString()
	if _, _, err := store.CreateUpload(channelID, owner, declaration); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteUpload(owner, channelID)
	if err != nil || deleted.State != StateDeleted || deleted.Manifest != nil {
		t.Fatalf("delete=%+v err=%v", deleted, err)
	}
	if _, _, err := store.CreateUpload(uuid.NewString(), owner, declaration); err != ErrSlugTaken {
		t.Fatalf("deleted slug was recycled: %v", err)
	}
}

func TestUploadProjectionHidesReposAndMailboxes(t *testing.T) {
	store, declaration, owner := uploadFixture(t)
	channelID := uuid.NewString()
	if _, _, err := store.CreateUpload(channelID, owner, declaration); err != nil {
		t.Fatal(err)
	}
	projection, err := store.UploadProjection(channelID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{declaration.AuthorityRepoID, declaration.UploadRepoID, "A@example.com", "a@example.com", owner} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("projection leaked %q: %s", forbidden, body)
		}
	}
	if err := projection.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveUploadAddress("atmprojekt", declaration.Slug)
	if err != nil || resolved.ChannelID != channelID {
		t.Fatalf("resolve=%+v err=%v", resolved, err)
	}
}

func TestCreateUploadRetryReturnsSameToken(t *testing.T) {
	store, declaration, owner := uploadFixture(t)
	channelID := uuid.NewString()
	first, firstDeliveries, err := store.CreateUpload(channelID, owner, declaration)
	if err != nil {
		t.Fatal(err)
	}
	second, secondDeliveries, err := store.CreateUpload(channelID, owner, declaration)
	if err != nil {
		t.Fatal(err)
	}
	if first.ChannelID != second.ChannelID || len(secondDeliveries) != 1 || firstDeliveries[0].Token != secondDeliveries[0].Token {
		t.Fatalf("retry changed identity or token")
	}
}
