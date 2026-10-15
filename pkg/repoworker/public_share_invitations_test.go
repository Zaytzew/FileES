package repoworker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/public-shares/channel"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

func TestPublicShareInvitationMessageAndExplicitResend(t *testing.T) {
	owner, repo, id := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &channel.Store{Root: t.TempDir(), Authority: shareAuthority{owner: owner, repo: repo, alias: "acme"}, TokenKey: []byte(strings.Repeat("t", 32))}
	outbox := PublicShareOutbox{Root: t.TempDir()}
	service := ChannelPublicShareService{Channels: store, Deliverer: outbox}
	d := shareDeclaration(repo)
	d.ManageInvitations, d.InvitationMessage = true, "Proszę o uwagę.\nZałączniki do projektu."
	if _, err := service.Create(t.Context(), id, owner, d); err != nil {
		t.Fatal(err)
	}
	job, ok, err := outbox.Claim(time.Now(), time.Minute)
	if err != nil || !ok || job.InvitationMessage != d.InvitationMessage {
		t.Fatalf("initial mail: %+v %v", job, err)
	}
	mail, err := RenderPublicShareMail(job, "sender@example.test", "example.test", "https://files.example.test")
	if err != nil || !strings.Contains(string(mail), "Proszę o uwagę.\r\nZałączniki") {
		t.Fatalf("mail body %v", err)
	}
	if err := outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	d.InvitationMessage = "Nowa wiadomość"
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, id, d, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := outbox.Claim(time.Now(), time.Minute); err != nil || ok {
		t.Fatal("message edit sent mail", err)
	}
	legacy := d
	legacy.ManageInvitations = false
	legacy.InvitationMessage = ""
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, id, legacy, false); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(t.Context(), owner, repo)
	if err != nil || listed[0].InvitationMessage != d.InvitationMessage {
		t.Fatal("legacy edit erased message", err)
	}
	before, err := store.ListOwned(owner, repo)
	if err != nil {
		t.Fatal(err)
	}
	d.ResendInvitations = true
	operation := uuid.NewString()
	if result, err := service.Update(t.Context(), operation, owner, id, d, false); err != nil || result.RecipientDeliveries != 1 {
		t.Fatal(result, err)
	}
	resent, ok, err := outbox.Claim(time.Now(), time.Minute)
	if err != nil || !ok || resent.Invitation != job.Invitation || resent.MessageID == job.MessageID || resent.InvitationMessage != d.InvitationMessage {
		t.Fatal("resend changed token or lost message", err)
	}
	after, _ := store.ListOwned(owner, repo)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("resend changed policy/credentials/timestamps")
	}
	if _, err := service.Update(t.Context(), operation, owner, id, d, false); err != nil {
		t.Fatal("lease retry", err)
	}
	if err := outbox.MarkSent(resent.MessageID, resent.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(t.Context(), operation, owner, id, d, false); err != nil {
		t.Fatal("completed retry", err)
	}
	if _, ok, err := outbox.Claim(time.Now().Add(time.Hour), time.Minute); err != nil || ok {
		t.Fatal("completed resend duplicated", err)
	}
	if _, err := service.Update(t.Context(), uuid.NewString(), uuid.NewString(), id, d, false); !errors.Is(err, ErrPublicShareRejected) {
		t.Fatal("foreign owner accepted", err)
	}
	stale := d
	stale.InvitationMessage = "stale"
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, id, stale, false); !errors.Is(err, ErrPublicShareRejected) {
		t.Fatal("stale resend accepted", err)
	}
	stale = d
	stale.Recipients = []string{"new@example.test"}
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, id, stale, false); !errors.Is(err, ErrPublicShareRejected) {
		t.Fatal("recipient mutation accepted", err)
	}
	if _, err := service.Create(t.Context(), uuid.NewString(), owner, d); !errors.Is(err, ErrPublicShareRejected) {
		t.Fatal("create/resend accepted", err)
	}
	if _, err := service.Revoke(t.Context(), owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, id, d, false); !errors.Is(err, ErrPublicShareRejected) {
		t.Fatal("revoked resend accepted", err)
	}
}

func TestPublicShareInvitationNegotiationAndValidation(t *testing.T) {
	shares := []control.PublicShareSummary{{InvitationMessage: "private message"}}
	legacy := publicShareListForClient(shares, true, false)
	raw, _ := json.Marshal(legacy)
	if strings.Contains(string(raw), "invitation") || shares[0].InvitationMessage == "" {
		t.Fatal("legacy protocol leaked options or mutated source")
	}
	modern := publicShareListForClient(shares, true, true)
	if !modern.CanManageInvitations || modern.Shares[0].InvitationMessage == "" {
		t.Fatal("modern options lost")
	}
	if publicShareListForClient(shares, false, true).CanManageInvitations {
		t.Fatal("no deliverer advertised")
	}
	for _, message := range []string{strings.Repeat("x", 2001), "bad\x00body"} {
		s := toShare(uuid.NewString(), shareDeclaration(uuid.NewString()))
		s.InvitationMessage = message
		if s.Validate() == nil {
			t.Fatal("invalid message accepted")
		}
	}
}

func TestPublicShareResendInterruptedCompletionAndContentFence(t *testing.T) {
	now := time.Now().UTC()
	o := PublicShareOutbox{Root: t.TempDir()}
	r := channel.Record{ChannelID: uuid.NewString(), Alias: "acme", Slug: "test-share", MailOperationID: uuid.NewString(), Manifest: &manifest.Share{InvitationMessage: "hello"}}
	d := []channel.Delivery{{Email: "a@example.test", Token: strings.Repeat("t", 43)}}
	if err := o.DeliverPublicShareTokens(t.Context(), r, d); err != nil {
		t.Fatal(err)
	}
	job, ok, err := o.Claim(now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := o.DeliverPublicShareTokens(t.Context(), r, d); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.Claim(now, time.Minute); err != nil || ok {
		t.Fatal("live lease replayed", err)
	}
	if err := o.MarkSent(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(o.Root, job.MessageID+".json"), job); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.Claim(now.Add(time.Hour), time.Minute); err != nil || ok {
		t.Fatal("completed leftover replayed", err)
	}
	r.Manifest.InvitationMessage = "changed"
	if err := o.DeliverPublicShareTokens(t.Context(), r, d); err == nil {
		t.Fatal("same operation changed content")
	}
	if _, err := os.Stat(filepath.Join(o.Root, job.MessageID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed job not removed")
	}
	r.MailOperationID = uuid.NewString()
	if err := o.DeliverPublicShareTokens(t.Context(), r, d); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.Claim(now.Add(time.Hour), time.Minute); err != nil || !ok {
		t.Fatal("new explicit resend suppressed", err)
	}
}
