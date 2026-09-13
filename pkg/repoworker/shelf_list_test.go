package repoworker

import (
	"context"
	"errors"
	control "filees/pkg/control/v1"
	"strings"
	"testing"
	"time"

	"filees/pkg/realmbranding"
	"filees/public-shares/channel"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type shelfAuthority struct{ owner, repo, alias string }

func TestWorkerRoutesShelfListAndEnforcesOwner(t *testing.T) {
	service, owner, channelID := shelfFixture(t)
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{UploadChannels: service, Store: store}
	clientID := uuid.NewString()
	for _, tc := range []struct {
		name, realm     string
		allowed, wantOK bool
	}{
		{"owner", owner, true, true},
		{"foreign", uuid.NewString(), true, false},
		{"no capability", owner, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketListShelf, clientID, control.ListShelfPayload{ChannelID: channelID}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			result, err := w.Handle(context.Background(), Session{ClientID: clientID, RealmID: tc.realm, CanCreateRepositories: tc.allowed}, ticket)
			if err != nil {
				t.Fatal(err)
			}
			if (result.Status == control.ResultOK) != tc.wantOK {
				t.Fatalf("result=%+v", result)
			}
			if tc.wantOK {
				var listing control.ListShelfResult
				if err := control.DecodeResultPayload(result.Result, &listing); err != nil {
					t.Fatal(err)
				}
				if listing.ChannelID != channelID {
					t.Fatalf("wrong shelf: %+v", listing)
				}
			}
		})
	}
}

func (a shelfAuthority) OwnsActiveRepository(realmID, repoID string) error {
	if realmID != a.owner || repoID != a.repo {
		return errors.New("not owner")
	}
	return nil
}

func (a shelfAuthority) ActiveRealmAlias(realmID string) (string, error) {
	if realmID != a.owner {
		return "", errors.New("not owner")
	}
	return a.alias, nil
}

func (a shelfAuthority) ActiveRealmBranding(string) (realmbranding.Branding, error) {
	return realmbranding.Default(), nil
}

func shelfFixture(t *testing.T) (ChannelUploadService, string, string) {
	t.Helper()
	owner, authorityRepo, uploadRepo := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &channel.Store{
		Root: t.TempDir(), Authority: shelfAuthority{owner: owner, repo: authorityRepo, alias: "atmprojekt"},
		TokenKey: []byte(strings.Repeat("t", 32)),
	}
	created, _, err := store.CreateUpload(uuid.NewString(), owner, manifest.Upload{
		OwnerRealm: owner, AuthorityRepoID: authorityRepo, UploadRepoID: uploadRepo,
		Slug: "oferta-a", Recipients: []string{"a@example.com"}, CollisionPolicy: manifest.CollisionDeny,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ChannelUploadService{Channels: store}, owner, created.ChannelID
}

func TestListShelfReturnsArrivalsForTheOwner(t *testing.T) {
	service, owner, channelID := shelfFixture(t)
	uploadID := uuid.NewString()
	err := service.Channels.RecordAccepted(channel.Accepted{
		ChannelID: channelID, UploadID: uploadID, RepoPath: "Opinia-Lodz.pdf",
		OriginalName: "Opinia Łódź.pdf", Size: 9, Revision: 42,
		AcceptedAt: time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ListShelf(context.Background(), owner, channelID)
	if err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != channelID || len(result.Items) != 1 {
		t.Fatalf("result=%+v", result)
	}
	item := result.Items[0]
	if item.UploadID != uploadID || item.RepoPath != "Opinia-Lodz.pdf" || item.Revision != 42 {
		t.Fatalf("item=%+v", item)
	}
	// The original name survives the seam intact: it is the contributor's
	// text and nothing on this path is allowed to reshape it.
	if item.OriginalName != "Opinia Łódź.pdf" {
		t.Fatalf("originalName=%q", item.OriginalName)
	}
	if item.AcceptedAt != "2026-09-12T10:00:00Z" {
		t.Fatalf("acceptedAt=%q", item.AcceptedAt)
	}
}

// A shelf receives from outside the system, so its listing says what strangers
// have sent to one owner. Serving it to another realm would make the gate leak
// backwards.
func TestListShelfRefusesAnotherRealm(t *testing.T) {
	service, _, channelID := shelfFixture(t)
	if err := service.Channels.RecordAccepted(channel.Accepted{
		ChannelID: channelID, UploadID: uuid.NewString(), RepoPath: "tajne.dwg",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.ListShelf(context.Background(), uuid.NewString(), channelID)
	if err == nil {
		t.Fatalf("a foreign realm was served the shelf: %+v", result)
	}
	if len(result.Items) != 0 {
		t.Fatalf("a refused listing still carried items: %+v", result)
	}
}

// An unknown channel is refused the same way a foreign one is, so guessing an
// identifier tells the guesser nothing about whether it exists.
func TestListShelfRefusesAnUnknownChannel(t *testing.T) {
	service, owner, _ := shelfFixture(t)
	if _, err := service.ListShelf(context.Background(), owner, uuid.NewString()); err == nil {
		t.Fatal("an unknown channel was listed without error")
	}
}

// A channel that has never received anything lists empty rather than failing:
// an empty shelf is an ordinary state, and the browser has to render it.
func TestListShelfOnAnEmptyShelf(t *testing.T) {
	service, owner, channelID := shelfFixture(t)
	result, err := service.ListShelf(context.Background(), owner, channelID)
	if err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != channelID || len(result.Items) != 0 {
		t.Fatalf("result=%+v", result)
	}
}
