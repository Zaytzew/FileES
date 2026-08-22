package repoworker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/public-shares/channel"
	"github.com/google/uuid"
)

func TestUploadChannelOutboxLeaseRenderAndRemoval(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	root := t.TempDir()
	outbox := UploadChannelOutbox{Root: root, Now: func() time.Time { return now }}
	record := channel.UploadRecord{ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "oferta-a"}
	delivery := channel.Delivery{Email: "A@example.com", Token: strings.Repeat("t", 43)}
	if err := outbox.DeliverUploadTokens(context.Background(), record, []channel.Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	jsonFiles := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		jsonFiles++
		info, _ := entry.Info()
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("outbox mode=%o", info.Mode().Perm())
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
	message, err := RenderUploadChannelMail(job, "filees@example.test", "example.test", "https://get.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), "/atmprojekt/oferta-a?invite=") || strings.Contains(string(message), "Bcc:") {
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

func TestUploadChannelOutboxPermanentRejectIsNotRetried(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	root := t.TempDir()
	outbox := UploadChannelOutbox{Root: root, Now: func() time.Time { return now }}
	record := channel.UploadRecord{ChannelID: uuid.NewString(), Alias: "atmprojekt", Slug: "oferta-a"}
	if err := outbox.DeliverUploadTokens(context.Background(), record, []channel.Delivery{{Email: "a@example.test", Token: strings.Repeat("t", 43)}}); err != nil {
		t.Fatal(err)
	}
	job, ok, err := outbox.Claim(now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%+v %v %v", job, ok, err)
	}
	if err := outbox.MarkRejected(job.MessageID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, retry, err := outbox.Claim(now.Add(time.Minute), time.Minute); err != nil || retry {
		t.Fatalf("rejected job was retried: %v %v", retry, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, job.MessageID+".json"))
	if err != nil || !strings.Contains(string(raw), `"state": "failed"`) {
		t.Fatalf("rejected job=%s err=%v", raw, err)
	}
}

func TestUploadChannelOutboxClaimMissingRootIsIdle(t *testing.T) {
	outbox := UploadChannelOutbox{Root: filepath.Join(t.TempDir(), "missing")}
	job, ok, err := outbox.Claim(time.Now(), time.Minute)
	if err != nil || ok || job.MessageID != "" {
		t.Fatalf("missing root claim=%+v %v %v", job, ok, err)
	}
}
