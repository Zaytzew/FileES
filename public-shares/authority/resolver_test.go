package authority

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"filees/pkg/realmbranding"
	"filees/public-shares/channel"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type testAuthority struct{ owner, repo, alias string }

func (a *testAuthority) OwnsActiveRepository(owner, repo string) error {
	if owner != a.owner || repo != a.repo {
		return errors.New("not owner")
	}
	return nil
}
func (a *testAuthority) ActiveRealmAlias(owner string) (string, error) {
	if owner != a.owner {
		return "", errors.New("not owner")
	}
	return a.alias, nil
}
func (a *testAuthority) ActiveRealmBranding(owner string) (realmbranding.Branding, error) {
	if owner != a.owner {
		return realmbranding.Branding{}, errors.New("not owner")
	}
	return realmbranding.Default(), nil
}

type testSource struct {
	head     int64
	content  map[int64]string
	lastRepo string
	lastPath string
}

func (s *testSource) Head(context.Context, string) (int64, error) { return s.head, nil }
func (s *testSource) Cat(_ context.Context, repo, path string, revision int64, dst io.Writer) error {
	value, ok := s.content[revision]
	if !ok {
		return errors.New("missing revision")
	}
	s.lastRepo, s.lastPath = repo, path
	_, err := io.WriteString(dst, value)
	return err
}

func resolverFixture(t *testing.T, pinned *int64) (*Resolver, *channel.Store, manifest.Share, string) {
	t.Helper()
	owner, repo := uuid.NewString(), uuid.NewString()
	authority := &testAuthority{owner: owner, repo: repo, alias: "atmprojekt"}
	store := &channel.Store{Root: t.TempDir(), Authority: authority, TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }}
	share := manifest.Share{
		OwnerRealm: owner, RepoID: repo, SourceRoot: "wydanie", Slug: "przetarg-2026", DoNotFollow: pinned,
		Objects: []manifest.Object{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "wydanie/projekt.pdf", DisplayName: "Projekt.pdf"}},
	}
	channelID := uuid.NewString()
	if _, _, err := store.Create(channelID, owner, share); err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{Channels: store, Source: &testSource{head: 5, content: map[int64]string{3: "revision three", 5: "revision five"}}, FrostKey: []byte(strings.Repeat("k", 32)), StagingRoot: t.TempDir(), MaxLeafSize: 1 << 20}
	return resolver, store, share, channelID
}

func TestEntryFrostAndFetchUseOnlyCanonicalMapping(t *testing.T) {
	resolver, _, share, _ := resolverFixture(t, nil)
	entry, err := resolver.Enter(context.Background(), "atmprojekt", "przetarg-2026")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Revision != 5 || entry.FrostProof == "" {
		t.Fatalf("entry = %+v", entry)
	}
	request := ObjectRequest{ChannelID: entry.Projection.ChannelID, PublicID: share.Objects[0].PublicID, Revision: entry.Revision, FrostProof: entry.FrostProof}
	permit, err := resolver.Check(context.Background(), request)
	if err != nil || len(permit.CacheKey) != 64 || permit.DisplayName != "Projekt.pdf" {
		t.Fatalf("permit = %+v, %v", permit, err)
	}
	leaf, err := resolver.Fetch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Body.Close()
	raw, err := io.ReadAll(leaf.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "revision five" || leaf.Size != int64(len(raw)) || len(leaf.MD5) != 32 {
		t.Fatalf("leaf = size=%d md5=%q body=%q", leaf.Size, leaf.MD5, raw)
	}
	source := resolver.Source.(*testSource)
	if source.lastRepo != share.RepoID || source.lastPath != share.Objects[0].RepoPath {
		t.Fatalf("source request = repo %q path %q", source.lastRepo, source.lastPath)
	}
}

func TestRevisionNumberWithoutMatchingFrostProofIsNotAuthority(t *testing.T) {
	resolver, _, share, _ := resolverFixture(t, nil)
	entry, err := resolver.Enter(context.Background(), "atmprojekt", share.Slug)
	if err != nil {
		t.Fatal(err)
	}
	request := ObjectRequest{ChannelID: entry.Projection.ChannelID, PublicID: share.Objects[0].PublicID, Revision: 3, FrostProof: entry.FrostProof}
	if _, err := resolver.Check(context.Background(), request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("arbitrary historical revision accepted: %v", err)
	}
}

func TestManifestUpdateAndRevokeInvalidatePriorFrost(t *testing.T) {
	resolver, store, share, channelID := resolverFixture(t, nil)
	entry, err := resolver.Enter(context.Background(), "atmprojekt", share.Slug)
	if err != nil {
		t.Fatal(err)
	}
	request := ObjectRequest{ChannelID: channelID, PublicID: share.Objects[0].PublicID, Revision: entry.Revision, FrostProof: entry.FrostProof}
	share.Objects[0].RepoPath = "wydanie/inny.pdf"
	if _, _, err := store.Update(uuid.NewString(), share.OwnerRealm, channelID, share); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Check(context.Background(), request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old frost survived manifest update: %v", err)
	}
	newEntry, err := resolver.Enter(context.Background(), "atmprojekt", share.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Revoke(share.OwnerRealm, channelID); err != nil {
		t.Fatal(err)
	}
	request.FrostProof = newEntry.FrostProof
	if _, err := resolver.Check(context.Background(), request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked channel remained visible: %v", err)
	}
}

func TestPinnedEntryUsesDeclaredRevision(t *testing.T) {
	pinned := int64(3)
	resolver, _, share, _ := resolverFixture(t, &pinned)
	entry, err := resolver.Enter(context.Background(), "atmprojekt", share.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Revision != pinned {
		t.Fatalf("revision = %d, want %d", entry.Revision, pinned)
	}
}

func TestSVNLookSourceRejectsNonCanonicalPathsBeforeExecution(t *testing.T) {
	source := SVNLookSource{SVNLook: "/does/not/exist", RepositoriesRoot: t.TempDir()}
	repoID := uuid.NewString()
	for _, path := range []string{"../secret", "dir/../secret", "dir/./file", "dir//file", "/file", "file/", " file"} {
		if err := source.Cat(context.Background(), repoID, path, 1, io.Discard); err == nil || strings.Contains(err.Error(), "fork/exec") {
			t.Fatalf("path %q reached executable: %v", path, err)
		}
	}
}

func TestFetchRejectsOversizedLeafAndCleansStaging(t *testing.T) {
	resolver, _, share, _ := resolverFixture(t, nil)
	resolver.MaxLeafSize = 5
	entry, err := resolver.Enter(context.Background(), "atmprojekt", share.Slug)
	if err != nil {
		t.Fatal(err)
	}
	request := ObjectRequest{ChannelID: entry.Projection.ChannelID, PublicID: share.Objects[0].PublicID, Revision: entry.Revision, FrostProof: entry.FrostProof}
	if _, err := resolver.Fetch(context.Background(), request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oversized leaf fetch = %v", err)
	}
	entries, err := os.ReadDir(resolver.StagingRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("authority staging after oversized leaf = %v, %v", entries, err)
	}
}
