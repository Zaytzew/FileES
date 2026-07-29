package servertool

import (
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

func TestRecoveryCleanupRemovesKeyBeforeExpiredReceiptAndIsIdempotent(t *testing.T) {
	results := t.TempDir()
	created := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	manifest := repoworker.RecoveryManifest{
		Schema: repoworker.RecoveryManifestSchema, OperationID: uuid.NewString(), RealmID: uuid.NewString(),
		Archives: []repoworker.RecoveryArchive{{
			ArchiveID: uuid.NewString(), RepoID: uuid.NewString(),
			SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 1,
		}},
		CreatedAt: created, DownloadUntil: created.Add(time.Hour), AdminGraceUntil: created.Add(2 * time.Hour),
	}
	manifests := repoworker.RecoveryManifestStore{Root: filepath.Join(results, "recovery-manifests")}
	keys := repoworker.RecoveryKeyStore{Root: filepath.Join(results, "recovery-keys")}
	if err := manifests.Save(manifest); err != nil {
		t.Fatal(err)
	}
	publicKey := testRecoveryPublicKey(t)
	if _, err := keys.Bind(manifest, publicKey); err != nil {
		t.Fatal(err)
	}
	if removed, err := reapRecoveryCapabilities(results, manifest.AdminGraceUntil.Add(-time.Second)); err != nil || len(removed) != 0 {
		t.Fatalf("premature cleanup=%v err=%v", removed, err)
	}
	if removed, err := reapRecoveryCapabilities(results, manifest.AdminGraceUntil); err != nil || len(removed) != 1 || removed[0] != manifest.OperationID {
		t.Fatalf("cleanup=%v err=%v", removed, err)
	}
	if _, err := manifests.Load(manifest.OperationID); err == nil {
		t.Fatal("expired manifest survived")
	}
	if _, err := keys.FindByPublicKey(publicKey, created); err == nil {
		t.Fatal("expired public capability survived")
	}
	if removed, err := reapRecoveryCapabilities(results, manifest.AdminGraceUntil); err != nil || len(removed) != 0 {
		t.Fatalf("idempotent cleanup=%v err=%v", removed, err)
	}
}

func TestRecoveryCleanupAcceptsMissingState(t *testing.T) {
	if removed, err := reapRecoveryCapabilities(filepath.Join(t.TempDir(), "missing"), time.Now()); err != nil || len(removed) != 0 {
		t.Fatalf("missing cleanup=%v err=%v", removed, err)
	}
}
