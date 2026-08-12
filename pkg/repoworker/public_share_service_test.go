package repoworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/realmbranding"
	"filees/public-shares/channel"
	"github.com/google/uuid"
)

type shareAuthority struct{ owner, repo, alias string }

func (a shareAuthority) OwnsActiveRepository(owner, repo string) error {
	if owner != a.owner || repo != a.repo {
		return errors.New("not owner")
	}
	return nil
}
func (a shareAuthority) ActiveRealmAlias(owner string) (string, error) {
	if owner != a.owner {
		return "", errors.New("not owner")
	}
	return a.alias, nil
}

func (a shareAuthority) ActiveRealmBranding(string) (realmbranding.Branding, error) {
	return realmbranding.Default(), nil
}

type shareDeliverer struct {
	calls  int
	fail   bool
	tokens []string
}

func (d *shareDeliverer) DeliverPublicShareTokens(_ context.Context, _ channel.Record, deliveries []channel.Delivery) error {
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

func shareDeclaration(repo string) control.PublicShareDeclaration {
	return control.PublicShareDeclaration{RepoID: repo, SourceRoot: "wydanie", Slug: "przetarg-2026", Recipients: []string{"a@example.com"}, Objects: []control.PublicShareObject{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "wydanie/projekt.pdf", DisplayName: "Projekt.pdf"}}}
}

func TestPublicShareDeliveryRetryRegeneratesSameToken(t *testing.T) {
	owner, repo, operationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &channel.Store{Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "atmprojekt"}, TokenKey: []byte(strings.Repeat("t", 32)), Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }}
	deliverer := &shareDeliverer{fail: true}
	service := ChannelPublicShareService{Channels: store, Deliverer: deliverer}
	if _, err := service.Create(context.Background(), operationID, owner, shareDeclaration(repo)); err == nil {
		t.Fatal("temporary delivery failure was hidden")
	}
	result, err := service.Create(context.Background(), operationID, owner, shareDeclaration(repo))
	if err != nil {
		t.Fatal(err)
	}
	if deliverer.calls != 2 || len(deliverer.tokens) != 2 || deliverer.tokens[0] != deliverer.tokens[1] {
		t.Fatalf("retry did not regenerate the same token: calls=%d tokens=%v", deliverer.calls, deliverer.tokens)
	}
	if result.State != channel.StateActive || result.RecipientDeliveries != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestPublicShareServiceRejectsNonOwner(t *testing.T) {
	owner, repo := uuid.NewString(), uuid.NewString()
	store := &channel.Store{Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "atmprojekt"}, TokenKey: []byte(strings.Repeat("t", 32))}
	service := ChannelPublicShareService{Channels: store, Deliverer: &shareDeliverer{}}
	if _, err := service.Create(context.Background(), uuid.NewString(), uuid.NewString(), shareDeclaration(repo)); !errors.Is(err, ErrPublicShareRejected) {
		t.Fatalf("non-owner create = %v", err)
	}
}

func TestPublicShareServiceClassifiesInvalidDeclarationAsRejected(t *testing.T) {
	owner, repo := uuid.NewString(), uuid.NewString()
	store := &channel.Store{Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "atmprojekt"}, TokenKey: []byte(strings.Repeat("t", 32))}
	declaration := shareDeclaration(repo)
	declaration.Objects[0].RepoPath = "../secret"
	_, err := (ChannelPublicShareService{Channels: store, Deliverer: &shareDeliverer{}}).Create(context.Background(), uuid.NewString(), owner, declaration)
	if !errors.Is(err, ErrPublicShareRejected) {
		t.Fatalf("invalid declaration classification = %v", err)
	}
}

func TestPublicShareServiceListsProtectedChannelsAndPreservesVerifier(t *testing.T) {
	owner, repo := uuid.NewString(), uuid.NewString()
	store := &channel.Store{Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "atmprojekt"}, TokenKey: []byte(strings.Repeat("t", 32))}
	service := ChannelPublicShareService{Channels: store, Deliverer: &shareDeliverer{}}
	declaration := shareDeclaration(repo)
	declaration.Recipients = nil
	declaration.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	channelID := uuid.NewString()
	if _, err := service.Create(context.Background(), channelID, owner, declaration); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background(), owner, repo)
	if err != nil || len(listed) != 1 || listed[0].ChannelID != channelID || !listed[0].PasswordProtected {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	declaration.PasswordHash = ""
	declaration.Objects[0].DisplayName = "Aktualizacja.pdf"
	if _, err := service.Update(context.Background(), uuid.NewString(), owner, channelID, declaration, true); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(context.Background(), owner, repo)
	if err != nil || len(listed) != 1 || !listed[0].PasswordProtected || listed[0].Objects[0].DisplayName != "Aktualizacja.pdf" {
		t.Fatalf("updated list=%+v err=%v", listed, err)
	}
}

type fakePublicShares struct {
	calls       int
	owner       string
	declaration control.PublicShareDeclaration
}

func (s *fakePublicShares) List(context.Context, string, string) ([]control.PublicShareSummary, error) {
	return nil, errors.New("unused")
}

func (s *fakePublicShares) Create(_ context.Context, operationID, owner string, declaration control.PublicShareDeclaration) (control.PublicShareResult, error) {
	s.calls++
	s.owner, s.declaration = owner, declaration
	return control.PublicShareResult{ChannelID: operationID, Alias: "atmprojekt", Slug: declaration.Slug, State: "active"}, nil
}
func (s *fakePublicShares) Update(context.Context, string, string, string, control.PublicShareDeclaration, bool) (control.PublicShareResult, error) {
	return control.PublicShareResult{}, errors.New("unused")
}
func (s *fakePublicShares) Revoke(context.Context, string, string) (control.PublicShareResult, error) {
	return control.PublicShareResult{}, errors.New("unused")
}
func (s *fakePublicShares) Delete(context.Context, string, string) (control.PublicShareResult, error) {
	return control.PublicShareResult{}, errors.New("unused")
}

func TestWorkerDerivesPublicShareOwnerAndAppliesCapabilityGate(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &fakePublicShares{}
	worker := Worker{Store: store, PublicShares: service}
	session := Session{ClientID: "client", RealmID: uuid.NewString(), CanCreateRepositories: true}
	declaration := shareDeclaration(uuid.NewString())
	ticket := mustPublicShareTicket(t, session.ClientID, declaration)
	result, err := worker.Handle(context.Background(), session, ticket)
	if err != nil || result.Status != control.ResultOK || service.calls != 1 || service.owner != session.RealmID {
		t.Fatalf("handle result=%+v err=%v calls=%d owner=%q", result, err, service.calls, service.owner)
	}
	session.CanCreateRepositories = false
	denied, err := worker.Handle(context.Background(), session, mustPublicShareTicket(t, session.ClientID, declaration))
	if err != nil || denied.Status != control.ResultError || denied.Error.Code != "PUBLIC_SHARE_FORBIDDEN" || service.calls != 1 {
		t.Fatalf("capability gate result=%+v err=%v calls=%d", denied, err, service.calls)
	}
}

func mustPublicShareTicket(t *testing.T, clientID string, declaration control.PublicShareDeclaration) control.Ticket {
	t.Helper()
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketCreatePublicShare, clientID, control.CreatePublicSharePayload{PublicShareDeclaration: declaration}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}
