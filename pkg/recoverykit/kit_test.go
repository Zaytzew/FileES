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
	kit, publicKey, err := Create("filees.example.net:22", "filees.example.net ssh-ed25519 AAAA", manifest)
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
}
