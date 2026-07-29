package recoverykit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

func TestCreateAndStoreRecoveryKit(t *testing.T) {
	now := time.Now().UTC()
	manifest := repoworker.RecoveryManifest{Schema: repoworker.RecoveryManifestSchema, OperationID: uuid.NewString(), RealmID: uuid.NewString(), CreatedAt: now, DownloadUntil: now.Add(time.Hour), AdminGraceUntil: now.Add(2 * time.Hour), Archives: []repoworker.RecoveryArchive{{ArchiveID: uuid.NewString(), RepoID: uuid.NewString(), SHA256: strings.Repeat("a", 64)}}}
	address := "filees.example.net:22"
	kit, publicKey, err := Create(address, knownHostLine(address, testRecoverySigner(t).PublicKey()), manifest)
	if err != nil || publicKey == "" || kit.Validate(now) != nil {
		t.Fatalf("kit=%+v key=%q err=%v validate=%v", kit, publicKey, err, kit.Validate(now))
	}
	path := filepath.Join(t.TempDir(), "recovery.fkr")
	if err := Store(path, kit); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "OPENSSH PRIVATE KEY") {
		t.Fatalf("kit contents err=%v", err)
	}
	loaded, err := Load(path, now)
	if err != nil || loaded.OperationID != manifest.OperationID || loaded.PublicKey != publicKey {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestDraftMustBeFinalizedWithMatchingManifest(t *testing.T) {
	operationID, realmID := uuid.NewString(), uuid.NewString()
	address := "filees.example.net:22"
	draft, publicKey, err := CreateDraft(address, knownHostLine(address, testRecoverySigner(t).PublicKey()), operationID, realmID)
	if err != nil || publicKey == "" {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	if err := draft.Validate(time.Now()); err == nil {
		t.Fatal("unfinished recovery kit accepted for download")
	}
	draftPath := filepath.Join(t.TempDir(), "pending.fkr")
	if err := Store(draftPath, draft); err != nil {
		t.Fatal(err)
	}
	if loaded, err := LoadDraft(draftPath); err != nil || loaded.PublicKey != publicKey {
		t.Fatalf("draft reload=%+v err=%v", loaded, err)
	}
	now := time.Now().UTC()
	manifest := repoworker.RecoveryManifest{Schema: repoworker.RecoveryManifestSchema, OperationID: operationID, RealmID: realmID, CreatedAt: now, DownloadUntil: now.Add(time.Hour), AdminGraceUntil: now.Add(2 * time.Hour)}
	final, err := Finalize(draft, manifest)
	if err != nil || final.Validate(now) != nil {
		t.Fatalf("final=%+v err=%v validate=%v", final, err, final.Validate(now))
	}
	manifest.OperationID = uuid.NewString()
	if _, err := Finalize(draft, manifest); err == nil {
		t.Fatal("foreign manifest finalized recovery kit")
	}
}
