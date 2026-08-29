package repoworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"filees/public-shares/channel"
	"github.com/google/uuid"
)

type uploadBackend struct {
	byOp     map[string]Repository
	creates  []string
	purposes []string
}

func (b *uploadBackend) Create(ctx context.Context, op, realm, name string) (Repository, error) {
	return b.CreateWithPurpose(ctx, op, realm, name, "")
}

func (b *uploadBackend) CreateWithPurpose(_ context.Context, op, _, name, purpose string) (Repository, error) {
	if b.byOp == nil {
		b.byOp = map[string]Repository{}
	}
	if repo, ok := b.byOp[op]; ok {
		b.creates = append(b.creates, name)
		b.purposes = append(b.purposes, purpose)
		return repo, nil
	}
	repo := Repository{RepoID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(op)).String(), URL: "svn+ssh://example/" + op}
	b.byOp[op] = repo
	b.creates = append(b.creates, name)
	b.purposes = append(b.purposes, purpose)
	return repo, nil
}

func (b *uploadBackend) Delete(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, errors.New("unused")
}

type uploadDeliverer struct {
	calls  int
	fail   bool
	tokens []string
}

func (d *uploadDeliverer) DeliverUploadTokens(_ context.Context, _ channel.UploadRecord, deliveries []channel.Delivery) error {
	d.calls++
	for _, delivery := range deliveries {
		d.tokens = append(d.tokens, delivery.Token)
	}
	if d.fail {
		d.fail = false
		return errors.New("temporary smtp failure")
	}
	return nil
}

func uploadStore(t *testing.T, owner, repo string) *channel.Store {
	t.Helper()
	return &channel.Store{
		Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "atmprojekt"},
		TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

func TestUploadChannelCreateProvisionsDistinctReposAndSharedTrash(t *testing.T) {
	owner, authority := uuid.NewString(), uuid.NewString()
	backend := &uploadBackend{}
	service := ChannelUploadService{Channels: uploadStore(t, owner, authority), Backend: backend, Deliverer: &uploadDeliverer{}}
	first, err := service.Create(context.Background(), uuid.NewString(), owner, control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "oferta-a", Recipients: []string{"a@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), uuid.NewString(), owner, control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "oferta-b", Recipients: []string{"b@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.UploadRepoID == "" || first.UploadRepoID == first.TrashRepoID || first.TrashRepoID != second.TrashRepoID || first.UploadRepoID == second.UploadRepoID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.State != channel.StateActive || second.Slug != "oferta-b" {
		t.Fatalf("results %+v %+v", first, second)
	}
	listed, err := service.List(context.Background(), owner, authority)
	if err != nil || len(listed) != 2 || listed[0].Kind != "shelf" || listed[1].Kind != "shelf" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if len(backend.purposes) < 4 || backend.purposes[0] != clientview.PurposeUploadTrash || backend.purposes[1] != clientview.PurposeUploadShelf {
		t.Fatalf("purposes=%v", backend.purposes)
	}
}

func TestUploadChannelCreateRejectsGuestAndAnonymous(t *testing.T) {
	owner, authority := uuid.NewString(), uuid.NewString()
	service := ChannelUploadService{Channels: uploadStore(t, owner, authority), Backend: &uploadBackend{}, Deliverer: &uploadDeliverer{}}
	if _, err := service.Create(context.Background(), uuid.NewString(), uuid.NewString(), control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "oferta-a", Recipients: []string{"a@example.com"}}); !errors.Is(err, ErrUploadChannelRejected) {
		t.Fatalf("guest create = %v", err)
	}
	if _, err := service.Create(context.Background(), uuid.NewString(), owner, control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "oferta-a"}); !errors.Is(err, ErrUploadChannelRejected) {
		t.Fatalf("anonymous create = %v", err)
	}
}

func TestUploadChannelCreateSharesSlugWithPublicShare(t *testing.T) {
	owner, authority := uuid.NewString(), uuid.NewString()
	store := uploadStore(t, owner, authority)
	share := ChannelPublicShareService{Channels: store, Deliverer: &shareDeliverer{}}
	if _, err := share.Create(context.Background(), uuid.NewString(), owner, control.PublicShareDeclaration{RepoID: authority, SourceRoot: ".", Slug: "wspolny-slug", Recipients: []string{"a@example.com"}, Objects: []control.PublicShareObject{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "a.pdf", DisplayName: "a.pdf"}}}); err != nil {
		t.Fatal(err)
	}
	service := ChannelUploadService{Channels: store, Backend: &uploadBackend{}, Deliverer: &uploadDeliverer{}}
	if _, err := service.Create(context.Background(), uuid.NewString(), owner, control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "wspolny-slug", Recipients: []string{"b@example.com"}}); !errors.Is(err, ErrUploadChannelRejected) {
		t.Fatalf("slug collision = %v", err)
	}
}

func TestUploadChannelDeliveryRetryKeepsTokenAndRepos(t *testing.T) {
	owner, authority, operationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	backend := &uploadBackend{}
	deliverer := &uploadDeliverer{fail: true}
	service := ChannelUploadService{Channels: uploadStore(t, owner, authority), Backend: backend, Deliverer: deliverer}
	declaration := control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "oferta-a", Recipients: []string{"a@example.com"}}
	if _, err := service.Create(context.Background(), operationID, owner, declaration); err == nil {
		t.Fatal("delivery failure was hidden")
	}
	result, err := service.Create(context.Background(), operationID, owner, declaration)
	if err != nil {
		t.Fatal(err)
	}
	if deliverer.calls != 2 || len(deliverer.tokens) != 2 || deliverer.tokens[0] != deliverer.tokens[1] {
		t.Fatalf("token retry: calls=%d tokens=%v", deliverer.calls, deliverer.tokens)
	}
	if result.UploadRepoID == "" || result.RecipientDeliveries != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestUploadChannelUpdateRejectsKindChange(t *testing.T) {
	owner, authority := uuid.NewString(), uuid.NewString()
	service := ChannelUploadService{Channels: uploadStore(t, owner, authority), Backend: &uploadBackend{}, Deliverer: &uploadDeliverer{}}
	created, err := service.Create(context.Background(), uuid.NewString(), owner, control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "oferta-a", Recipients: []string{"a@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), uuid.NewString(), owner, created.ChannelID, control.UploadChannelDeclaration{Recipients: []string{"a@example.com"}, Kind: "slots"}); !errors.Is(err, ErrUploadChannelRejected) {
		t.Fatalf("kind change: %v", err)
	}
}

func TestUploadChannelRevokeDoesNotDropRepos(t *testing.T) {
	owner, authority := uuid.NewString(), uuid.NewString()
	service := ChannelUploadService{Channels: uploadStore(t, owner, authority), Backend: &uploadBackend{}, Deliverer: &uploadDeliverer{}}
	created, err := service.Create(context.Background(), uuid.NewString(), owner, control.UploadChannelDeclaration{AuthorityRepoID: authority, Slug: "oferta-a", Recipients: []string{"a@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := service.Revoke(context.Background(), owner, created.ChannelID)
	if err != nil || revoked.State != channel.StateRevoked || revoked.UploadRepoID != created.UploadRepoID {
		t.Fatalf("revoke=%+v err=%v", revoked, err)
	}
}
