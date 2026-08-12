package channel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/realmbranding"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type fakeAuthority struct{ owner, repo, alias string }

func (a fakeAuthority) OwnsActiveRepository(realmID, repoID string) error {
	if realmID != a.owner || repoID != a.repo {
		return errors.New("not owner")
	}
	return nil
}
func (a fakeAuthority) ActiveRealmAlias(realmID string) (string, error) {
	if realmID != a.owner {
		return "", errors.New("not owner")
	}
	return a.alias, nil
}

func (a fakeAuthority) ActiveRealmBranding(string) (realmbranding.Branding, error) {
	return realmbranding.Default(), nil
}

func fixture(t *testing.T) (*Store, manifest.Share, string) {
	t.Helper()
	owner, repo := uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store := &Store{
		Root: t.TempDir(), Authority: fakeAuthority{owner: owner, repo: repo, alias: "atmprojekt"},
		TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return now },
	}
	share := manifest.Share{
		OwnerRealm: owner, RepoID: repo, SourceRoot: "wydanie", Slug: "przetarg-2026",
		Recipients: []string{"A@example.com"},
		Objects:    []manifest.Object{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "wydanie/projekt.pdf", DisplayName: "Projekt.pdf"}},
	}
	return store, share, owner
}

func TestCreateSeparatesRecordFromProjection(t *testing.T) {
	store, share, owner := fixture(t)
	channelID := uuid.NewString()
	record, deliveries, err := store.Create(channelID, owner, share)
	if err != nil {
		t.Fatal(err)
	}
	if record.Manifest == nil || record.Manifest.Objects[0].RepoPath != "wydanie/projekt.pdf" {
		t.Fatalf("canonical record lost repository path: %+v", record)
	}
	if len(deliveries) != 1 || deliveries[0].Email != "a@example.com" || deliveries[0].Token == "" {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	digest := sha256.Sum256([]byte(deliveries[0].Token))
	if got := record.Recipients[0].TokenHash; got != hex.EncodeToString(digest[:]) {
		t.Fatalf("token digest = %q", got)
	}
	projection, err := store.Projection(channelID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"repo_path", "source_root", share.RepoID, share.OwnerRealm, "wydanie/projekt.pdf", deliveries[0].Token} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public projection leaked %q: %s", forbidden, raw)
		}
	}
	info, err := os.Stat(filepath.Join(store.Root, "channels", channelID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("record mode = %o", info.Mode().Perm())
	}
}

func TestSlugTombstoneSurvivesDelete(t *testing.T) {
	store, share, owner := fixture(t)
	first := uuid.NewString()
	if _, _, err := store.Create(first, owner, share); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Delete(owner, first)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Manifest != nil || len(deleted.Recipients) != 0 || deleted.State != StateDeleted {
		t.Fatalf("deleted record retained private state: %+v", deleted)
	}
	if _, err := store.Projection(first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted channel projection = %v", err)
	}
	if _, _, err := store.Create(uuid.NewString(), owner, share); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("reused deleted slug: %v", err)
	}
}

func TestUpdatePreservesTokenAndRotatesReaddedRecipient(t *testing.T) {
	store, share, owner := fixture(t)
	channelID := uuid.NewString()
	record, _, err := store.Create(channelID, owner, share)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := record.Recipients[0].TokenHash
	share.Objects[0].DisplayName = "Projekt — aktualizacja.pdf"
	record, deliveries, err := store.Update(uuid.NewString(), owner, channelID, share)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 0 || record.Recipients[0].TokenHash != originalHash {
		t.Fatalf("unchanged recipient token rotated: %+v %+v", record, deliveries)
	}
	share.Recipients = nil
	if _, _, err := store.Update(uuid.NewString(), owner, channelID, share); err != nil {
		t.Fatal(err)
	}
	share.Recipients = []string{"a@example.com"}
	record, deliveries, err = store.Update(uuid.NewString(), owner, channelID, share)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || record.Recipients[0].TokenHash == originalHash {
		t.Fatalf("re-added recipient did not receive a fresh token: %+v %+v", record, deliveries)
	}
}

func TestOnlyRepositoryOwnerCanCreateOrMutate(t *testing.T) {
	store, share, owner := fixture(t)
	channelID := uuid.NewString()
	if _, _, err := store.Create(channelID, uuid.NewString(), share); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign create = %v", err)
	}
	if _, _, err := store.Create(channelID, owner, share); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Revoke(uuid.NewString(), channelID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign revoke = %v", err)
	}
	revoked, err := store.Revoke(owner, channelID)
	if err != nil || revoked.State != StateRevoked {
		t.Fatalf("owner revoke = %+v, %v", revoked, err)
	}
	if _, _, err := store.Update(uuid.NewString(), owner, channelID, share); !errors.Is(err, ErrInactive) {
		t.Fatalf("revoked channel update = %v", err)
	}
}

func TestHostPolicyBoundsChannelsAndMayRequirePassword(t *testing.T) {
	store, share, owner := fixture(t)
	store.MaxChannelsPerRealm = 1
	store.PasswordRequired = true
	share.Recipients = nil
	if _, _, err := store.Create(uuid.NewString(), owner, share); !errors.Is(err, ErrPolicy) {
		t.Fatalf("unpassworded open channel under required policy = %v", err)
	}
	share.Recipients = []string{"a@example.com"}
	first := uuid.NewString()
	if _, _, err := store.Create(first, owner, share); err != nil {
		t.Fatal(err)
	}
	share.Slug = "drugi-przetarg"
	if _, _, err := store.Create(uuid.NewString(), owner, share); !errors.Is(err, ErrPolicy) {
		t.Fatalf("realm channel limit = %v", err)
	}
	if _, err := store.Delete(owner, first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(uuid.NewString(), owner, share); err != nil {
		t.Fatalf("deleted channel did not release active quota: %v", err)
	}
}

func TestUpdatePreservingPasswordAndListOwnedDoNotExposeOrLosePolicy(t *testing.T) {
	store, share, owner := fixture(t)
	share.Recipients = nil
	share.Password = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	channelID := uuid.NewString()
	created, _, err := store.Create(channelID, owner, share)
	if err != nil {
		t.Fatal(err)
	}
	declaration := share
	declaration.Password = ""
	declaration.Objects[0].DisplayName = "Nowa nazwa.pdf"
	updated, _, err := store.UpdatePreservingPassword(uuid.NewString(), owner, channelID, declaration)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Manifest == nil || updated.Manifest.Password != created.Manifest.Password || updated.Manifest.Objects[0].DisplayName != "Nowa nazwa.pdf" {
		t.Fatalf("preserved update=%+v", updated)
	}
	listed, err := store.ListOwned(owner, share.RepoID)
	if err != nil || len(listed) != 1 || listed[0].ChannelID != channelID || listed[0].State != StateActive {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	if _, err := store.Revoke(owner, channelID); err != nil {
		t.Fatal(err)
	}
	listed, err = store.ListOwned(owner, share.RepoID)
	if err != nil || len(listed) != 1 || listed[0].State != StateRevoked {
		t.Fatalf("revoked list=%+v err=%v", listed, err)
	}
	if _, err := store.Delete(owner, channelID); err != nil {
		t.Fatal(err)
	}
	listed, err = store.ListOwned(owner, share.RepoID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("deleted list=%+v err=%v", listed, err)
	}
}
