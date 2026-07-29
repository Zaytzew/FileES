package repoworker

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestRecoveryKeyBindsOneManifestAndExpires(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	manifest := RecoveryManifest{Schema: RecoveryManifestSchema, OperationID: uuid.NewString(), RealmID: uuid.NewString(), CreatedAt: now, DownloadUntil: now.Add(time.Hour), AdminGraceUntil: now.Add(2 * time.Hour), Archives: []RecoveryArchive{{ArchiveID: uuid.NewString(), RepoID: uuid.NewString(), SHA256: strings.Repeat("a", 64)}}}
	store := RecoveryKeyStore{Root: t.TempDir()}
	record, err := store.Bind(manifest, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.FindByPublicKey(record.PublicKey, now)
	if err != nil || got.OperationID != manifest.OperationID {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := store.FindByPublicKey(record.PublicKey, manifest.DownloadUntil); err == nil {
		t.Fatal("expired recovery key accepted")
	}
	if _, err := store.Bind(manifest, record.PublicKey+" comment"); err == nil {
		t.Fatal("commented recovery key accepted")
	}
	if err := store.Remove(manifest.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(manifest.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindByPublicKey(record.PublicKey, now); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed key lookup=%v", err)
	}
}
