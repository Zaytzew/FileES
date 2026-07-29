package repoworker

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecoveryManifestStoreIsImmutableAndValidatesRetention(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store := RecoveryManifestStore{Root: t.TempDir()}
	manifest := RecoveryManifest{Schema: RecoveryManifestSchema, OperationID: uuid.NewString(), RealmID: uuid.NewString(), CreatedAt: now, DownloadUntil: now.Add(24 * time.Hour), AdminGraceUntil: now.Add(34 * time.Hour), Archives: []RecoveryArchive{{ArchiveID: uuid.NewString(), RepoID: uuid.NewString(), SHA256: strings.Repeat("a", 64), Size: 123}}}
	if err := store.Save(manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(manifest.OperationID)
	if err != nil || len(loaded.Archives) != 1 || loaded.Archives[0].Size != 123 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	manifest.Archives[0].Size++
	if err := store.Save(manifest); err == nil {
		t.Fatal("conflicting manifest accepted")
	}
	manifest.DownloadUntil = now.Add(-time.Second)
	if err := validateRecoveryManifest(manifest); err == nil {
		t.Fatal("invalid retention accepted")
	}
}
