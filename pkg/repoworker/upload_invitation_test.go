package repoworker

import (
	"context"
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

func TestShelfInvitationListKeepsLegacyStrictResponse(t *testing.T) {
	listed := control.ListUploadChannelsResult{CanManageInvitations: true, Channels: []control.UploadChannelSummary{{ChannelID: "channel", InvitationMessage: "Saved message"}}}
	legacy := uploadChannelListForClient(listed, false)
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "invitation_message") || strings.Contains(string(raw), "can_manage_invitations") {
		t.Fatal("new fields leaked to strict legacy consumer")
	}
	if listed.Channels[0].InvitationMessage == "" {
		t.Fatal("legacy formatting mutated authoritative listing")
	}
	modern := uploadChannelListForClient(listed, true)
	if !modern.CanManageInvitations || modern.Channels[0].InvitationMessage != "Saved message" {
		t.Fatal("opt-in lost invitation fields")
	}
}

// End-to-end service/store/outbox/render, with no SMTP or production data.
func TestShelfInvitationMessageAndExplicitResend(t *testing.T) {
	owner, repo := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	outbox := UploadChannelOutbox{Root: t.TempDir(), Now: func() time.Time { return now }}
	store := uploadStore(t, owner, repo)
	service := ChannelUploadService{Channels: store, Backend: &uploadBackend{}, Deliverer: outbox}
	body := control.UploadChannelDeclaration{AuthorityRepoID: repo, Slug: "test-shelf", Recipients: []string{"a@example.test", "b@example.test"}, ManageInvitations: true, InvitationMessage: "Proszę o rysunki.\nDziękuję!"}
	created, err := service.Create(t.Context(), uuid.NewString(), owner, body)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.GetUpload(created.ChannelID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		job, ok, err := outbox.Claim(now, time.Minute)
		if err != nil || !ok || job.InvitationMessage != body.InvitationMessage {
			t.Fatalf("initial job: %+v %v", job, err)
		}
		if err := outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	body.InvitationMessage = "Zaktualizowana wiadomość\nBcc: not-a-header@example.test"
	updated, err := service.Update(t.Context(), uuid.NewString(), owner, created.ChannelID, body)
	if err != nil || updated.RecipientDeliveries != 0 {
		t.Fatalf("message edit: %+v %v", updated, err)
	}
	if _, ok, err := outbox.Claim(now, time.Minute); err != nil || ok {
		t.Fatalf("edit sent mail: %v %v", ok, err)
	}
	// Legacy UPDATE preserves the owner's message.
	legacy := body
	legacy.ManageInvitations, legacy.InvitationMessage = false, ""
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, created.ChannelID, legacy); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(t.Context(), owner, repo)
	if err != nil || !listed.CanManageInvitations || listed.Channels[0].InvitationMessage != body.InvitationMessage {
		t.Fatalf("listing: %+v %v", listed, err)
	}
	body.ResendInvitations = true
	op := uuid.NewString()
	queued, err := service.Update(t.Context(), op, owner, created.ChannelID, body)
	if err != nil || queued.RecipientDeliveries != 2 {
		t.Fatalf("resend: %+v %v", queued, err)
	}
	for i := 0; i < 2; i++ {
		job, ok, err := outbox.Claim(now, time.Minute)
		if err != nil || !ok || job.ResendOperationID != op {
			t.Fatalf("resend job: %+v %v", job, err)
		}
		message, err := RenderUploadChannelMail(job, "filees@example.test", "example.test", "https://get.example.test")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(message), "\r\n\r\n", 2)
		if len(parts) != 2 || strings.Contains(parts[0], "Bcc:") || !strings.Contains(parts[1], "Bcc: not-a-header@example.test") {
			t.Fatal("message escaped body boundary")
		}
		if err := outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
			t.Fatal(err)
		}
		receipt, err := os.ReadFile(filepath.Join(outbox.Root, "sent", job.MessageID+".json"))
		if err != nil || strings.Contains(string(receipt), job.Invitation) || strings.Contains(string(receipt), "example.test") {
			t.Fatalf("receipt leaks credentials: %v", err)
		}
	}
	now = now.Add(time.Hour)
	if _, err := service.Update(t.Context(), op, owner, created.ChannelID, body); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := outbox.Claim(now, time.Minute); err != nil || ok {
		t.Fatalf("completed resend replayed: %v %v", ok, err)
	}
	after, _ := store.GetUpload(created.ChannelID)
	if !reflect.DeepEqual(initial.Recipients, after.Recipients) || initial.State != after.State {
		t.Fatal("resend rotated credentials or state")
	}
	if _, err := service.Update(t.Context(), uuid.NewString(), uuid.NewString(), created.ChannelID, body); !errors.Is(err, ErrUploadChannelRejected) {
		t.Fatalf("foreign owner: %v", err)
	}
	stale := body
	stale.InvitationMessage = "stale"
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, created.ChannelID, stale); !errors.Is(err, ErrUploadChannelRejected) {
		t.Fatalf("stale resend: %v", err)
	}
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, created.ChannelID, body); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := outbox.Claim(now, time.Minute); err != nil || !ok {
		t.Fatalf("fresh explicit resend unavailable: %v %v", ok, err)
	}
	if _, err := service.Revoke(context.Background(), owner, created.ChannelID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(t.Context(), uuid.NewString(), owner, created.ChannelID, body); !errors.Is(err, ErrUploadChannelRejected) {
		t.Fatalf("inactive resend: %v", err)
	}
}

func TestResendQueueReplayDuringLeaseAndInterruptedCompletion(t *testing.T) {
	now := time.Now().UTC()
	o := UploadChannelOutbox{Root: t.TempDir()}
	record := channel.UploadRecord{ChannelID: uuid.NewString(), Alias: "acme", Slug: "test-shelf", MailOperationID: uuid.NewString(), Manifest: &manifest.Upload{InvitationMessage: "Hello"}}
	deliveries := []channel.Delivery{{Email: "a@example.test", Token: strings.Repeat("t", 43)}}
	if err := o.DeliverUploadTokens(t.Context(), record, deliveries); err != nil {
		t.Fatal(err)
	}
	job, ok, err := o.Claim(now, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := o.DeliverUploadTokens(t.Context(), record, deliveries); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.Claim(now, time.Minute); err != nil || ok {
		t.Fatal("live lease replayed")
	}
	if err := o.MarkSent(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	// Crash after receipt, before removal: restart must not send the leftover job.
	if err := atomicJSON(filepath.Join(o.Root, job.MessageID+".json"), job); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.Claim(now.Add(time.Hour), time.Minute); err != nil || ok {
		t.Fatal("completed leftover replayed")
	}
	record.Manifest.InvitationMessage = "different"
	if err := o.DeliverUploadTokens(t.Context(), record, deliveries); err == nil {
		t.Fatal("same operation changed message")
	}
}
