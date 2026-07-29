package repoworker

import (
	"errors"
	"os"
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

func TestRecoveryManifestReapWaitsForGrace(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store := RecoveryManifestStore{Root: t.TempDir()}
	manifest := RecoveryManifest{Schema: RecoveryManifestSchema, OperationID: uuid.NewString(), RealmID: uuid.NewString(), CreatedAt: now, DownloadUntil: now.Add(time.Hour), AdminGraceUntil: now.Add(2 * time.Hour)}
	if err := store.Save(manifest); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.ReapExpired(manifest.AdminGraceUntil.Add(-time.Nanosecond)); err != nil || len(removed) != 0 {
		t.Fatalf("early reap=%v err=%v", removed, err)
	}
	if removed, err := store.ReapExpired(manifest.AdminGraceUntil); err != nil || len(removed) != 1 || removed[0] != manifest.OperationID {
		t.Fatalf("reap=%v err=%v", removed, err)
	}
	if _, err := store.Load(manifest.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed receipt load=%v", err)
	}
}
