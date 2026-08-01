package repoworker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/public-shares/channel"
	"github.com/google/uuid"
)

func TestPublicShareOutboxLeaseRenderAndRemoval(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	root := t.TempDir()
	outbox := PublicShareOutbox{Root: root, Now: func() time.Time { return now }}
	record := channel.Record{ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "przetarg-2026"}
	delivery := channel.Delivery{Email: "A@example.com", Token: strings.Repeat("t", 43)}
	if err := outbox.DeliverPublicShareTokens(context.Background(), record, []channel.Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	jsonFiles := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonFiles++
			info, _ := entry.Info()
			if info.Mode().Perm() != 0600 {
				t.Fatalf("outbox mode=%o", info.Mode().Perm())
			}
		}
	}
	if jsonFiles != 1 {
		t.Fatalf("outbox entries=%v", entries)
	}
	job, ok, err := outbox.Claim(now, 5*time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v %v %v", job, ok, err)
	}
	if _, second, err := outbox.Claim(now.Add(time.Minute), 5*time.Minute); err != nil || second {
		t.Fatalf("live lease claimed twice: %v %v", second, err)
	}
	message, err := RenderPublicShareMail(job, "filees@example.test", "example.test", "https://get.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), "/atmprojekt/przetarg-2026?token=") || strings.Contains(string(message), "Bcc:") {
		t.Fatalf("mail=%s", message)
	}
	if err := outbox.MarkFailed(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	job, ok, err = outbox.Claim(now.Add(time.Minute), 5*time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaim=%+v %v %v", job, ok, err)
	}
	if err := outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := outbox.Claim(now.Add(2*time.Minute), 5*time.Minute); err != nil || ok {
		t.Fatalf("sent job remained: %v %v", ok, err)
	}
}

func TestChannelRevokeAndRealmDeleteClearPendingMail(t *testing.T) {
	owner, repo := uuid.NewString(), uuid.NewString()
	stateRoot := t.TempDir()
	channels := &channel.Store{Root: stateRoot, Authority: shareAuthority{owner: owner, repo: repo, alias: "atmprojekt"}, TokenKey: []byte(strings.Repeat("t", 32))}
	outbox := PublicShareOutbox{Root: filepath.Join(stateRoot, "outbox")}
	service := ChannelPublicShareService{Channels: channels, Deliverer: outbox}
	firstID := uuid.NewString()
	if _, err := service.Create(context.Background(), firstID, owner, shareDeclaration(repo)); err != nil {
		t.Fatal(err)
	}
	if _, err := channels.Revoke(owner, firstID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := outbox.Claim(time.Now(), time.Minute); err != nil || ok {
		t.Fatalf("revoked channel retained mail: %v %v", ok, err)
	}
	second := shareDeclaration(repo)
	second.Slug = "drugi-przetarg"
	secondID := uuid.NewString()
	if _, err := service.Create(context.Background(), secondID, owner, second); err != nil {
		t.Fatal(err)
	}
	if count, err := channels.DeleteRealm(owner); err != nil || count != 2 {
		t.Fatalf("DeleteRealm=%d %v", count, err)
	}
	record, err := channels.Load(secondID)
	if err != nil || record.State != channel.StateDeleted || record.Manifest != nil || len(record.Recipients) != 0 {
		t.Fatalf("erased record=%+v %v", record, err)
	}
	if _, ok, err := outbox.Claim(time.Now(), time.Minute); err != nil || ok {
		t.Fatalf("realm erasure retained mail: %v %v", ok, err)
	}
	if count, err := channels.DeleteRealm(owner); err != nil || count != 0 {
		t.Fatalf("idempotent DeleteRealm=%d %v", count, err)
	}
}
