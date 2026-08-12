package recipientotp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/realmbranding"
	"filees/pkg/repoworker"
	"filees/public-shares/channel"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type testAuthority struct{ owner, repo string }

func (a testAuthority) OwnsActiveRepository(owner, repo string) error {
	if owner != a.owner || repo != a.repo {
		return errors.New("not owner")
	}
	return nil
}
func (a testAuthority) ActiveRealmAlias(string) (string, error) { return "realm", nil }
func (a testAuthority) ActiveRealmBranding(string) (realmbranding.Branding, error) {
	return realmbranding.Default(), nil
}

type fixture struct {
	service    Service
	store      *channel.Store
	invitation string
	owner      string
	share      manifest.Share
	now        *time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	now := time.Unix(1800000000, 0).UTC()
	owner, repo := uuid.NewString(), uuid.NewString()
	root := t.TempDir()
	store := &channel.Store{
		Root: root, Authority: testAuthority{owner: owner, repo: repo},
		TokenKey: []byte(strings.Repeat("i", 32)), Now: func() time.Time { return now },
	}
	share := manifest.Share{
		OwnerRealm: owner, RepoID: repo, SourceRoot: "shared", Slug: "files", Recipients: []string{"recipient@example.test"},
		Objects: []manifest.Object{{PublicID: "1234567890abcdef", RepoPath: "shared/file.txt", DisplayName: "file.txt"}},
	}
	_, deliveries, err := store.Create(uuid.NewString(), owner, share)
	if err != nil {
		t.Fatal(err)
	}
	outbox := repoworker.PublicShareOutbox{Root: filepath.Join(root, "outbox"), Now: func() time.Time { return now }}
	service := Service{
		Root: filepath.Join(root, "recipient-otp"), Key: []byte(strings.Repeat("o", 32)),
		Channels: store, Outbox: outbox, Now: func() time.Time { return now },
	}
	return fixture{service: service, store: store, invitation: deliveries[0].Token, owner: owner, share: share, now: &now}
}

func TestOTPActivatesOnRequestResendsWithoutRotationAndExpiresVisit(t *testing.T) {
	f := newFixture(t)
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	job, ok, err := f.service.Outbox.Claim(*f.now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v %v %v", job, ok, err)
	}
	grant, err := f.service.Verify(VerifyRequest{Request: request, Code: job.Code})
	if err != nil {
		t.Fatal(err)
	}
	if !grant.ExpiresAt.Equal(f.now.Add(5 * time.Minute)) {
		t.Fatalf("expiry=%s", grant.ExpiresAt)
	}
	if err := f.service.Outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(31 * time.Second)
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	resent, ok, err := f.service.Outbox.Claim(*f.now, time.Minute)
	if err != nil || !ok || resent.Code != job.Code {
		t.Fatalf("resend=%+v %v %v", resent, ok, err)
	}
	if err := f.service.Outbox.MarkSent(resent.MessageID, resent.AttemptID); err != nil {
		t.Fatal(err)
	}
	*f.now = grant.ExpiresAt
	if _, err := f.service.Verify(VerifyRequest{Request: request, Code: job.Code}); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired code accepted: %v", err)
	}
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	rotated, ok, err := f.service.Outbox.Claim(*f.now, time.Minute)
	if err != nil || !ok || rotated.MessageID == job.MessageID {
		t.Fatalf("rotation=%+v %v %v", rotated, ok, err)
	}
}

func TestOTPAttemptsAreBoundedAndInvitationCannotResetEpoch(t *testing.T) {
	f := newFixture(t)
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	job, ok, err := f.service.Outbox.Claim(*f.now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v %v %v", job, ok, err)
	}
	for range DefaultAttempts {
		if _, err := f.service.Verify(VerifyRequest{Request: request, Code: "00000000"}); !errors.Is(err, ErrDenied) {
			t.Fatalf("wrong code result=%v", err)
		}
	}
	if err := f.service.RequestCode(request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Verify(VerifyRequest{Request: request, Code: job.Code}); !errors.Is(err, ErrDenied) {
		t.Fatalf("request reset failed-attempt counter: %v", err)
	}
}

func TestRemovedRecipientIsRejectedImmediately(t *testing.T) {
	f := newFixture(t)
	request := Request{Alias: "realm", Slug: "files", Invitation: f.invitation}
	updated := f.share
	updated.Recipients = nil
	if _, _, err := f.store.Update(uuid.NewString(), f.owner, f.storeID(t), updated); err != nil {
		t.Fatal(err)
	}
	if err := f.service.RequestCode(request); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed invitation accepted: %v", err)
	}
}

func (f fixture) storeID(t *testing.T) string {
	t.Helper()
	records, err := f.store.ListOwned(f.owner, f.share.RepoID)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%v %v", len(records), err)
	}
	return records[0].ChannelID
}
