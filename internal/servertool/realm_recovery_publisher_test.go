package servertool

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func testRecoveryPublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestRealmRecoveryPublisherWithNoRetainedDumpCreatesNoCapability(t *testing.T) {
	root := t.TempDir()
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	confirmedAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	publicKey := testRecoveryPublicKey(t)
	manifests := repoworker.RecoveryManifestStore{Root: filepath.Join(root, "manifests")}
	keys := repoworker.RecoveryKeyStore{Root: filepath.Join(root, "keys")}
	record := repoworker.RealmRemovalRecord{
		OperationID: operationID, RealmID: realmID,
		Scope:       repoworker.RealmRemovalScope{OwnedRepoIDs: []string{repoID}},
		Request:     repoworker.RealmRemovalRequest{RecoveryPublicKey: publicKey},
		ConfirmedAt: &confirmedAt,
	}
	publisher := realmRecoveryPublisher{
		ArchiveRoot: filepath.Join(root, "absent-zero-retention-archives"),
		Manifests:   manifests, Keys: keys,
	}
	if err := publisher.Prepare(record); err != nil {
		t.Fatal(err)
	}
	manifest, err := manifests.Load(operationID)
	if err != nil || len(manifest.Archives) != 0 {
		t.Fatalf("zero-retention manifest=%+v err=%v", manifest, err)
	}
	if _, err := keys.FindByPublicKey(publicKey, confirmedAt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("zero-retention recovery key exists: %v", err)
	}
}

func TestRealmRecoveryPublisherBindsExactArchivesIdempotently(t *testing.T) {
	root := t.TempDir()
	archiveRoot := filepath.Join(root, "archives")
	repositoriesRoot := filepath.Join(root, "repositories")
	manifests := repoworker.RecoveryManifestStore{Root: filepath.Join(root, "manifests")}
	keys := repoworker.RecoveryKeyStore{Root: filepath.Join(root, "keys")}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	deleteOperationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":"+repoID+":delete")).String()
	created := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	svnadmin := requireSVN(t, "svnadmin")[0]
	if err := os.MkdirAll(repositoriesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnadmin, "create", filepath.Join(repositoriesRoot, repoID)).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, output)
	}
	effects := repoworker.ServerEffects{
		SVNAdmin: svnadmin, RepositoriesRoot: repositoriesRoot,
		DeletionArchiveRoot: archiveRoot, DeletionRetentionDays: 7,
		Now: func() time.Time { return created },
	}
	if _, err := effects.ArchiveAndDeleteFSFS(context.Background(), repoID, deleteOperationID); err != nil {
		t.Fatal(err)
	}
	confirmedAt := created
	record := repoworker.RealmRemovalRecord{
		OperationID: operationID, RealmID: realmID,
		Scope:       repoworker.RealmRemovalScope{OwnedRepoIDs: []string{repoID}},
		Request:     repoworker.RealmRemovalRequest{RecoveryPublicKey: testRecoveryPublicKey(t)},
		ConfirmedAt: &confirmedAt,
	}
	publisher := realmRecoveryPublisher{ArchiveRoot: archiveRoot, Manifests: manifests, Keys: keys}
	if err := publisher.Prepare(record); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Prepare(record); err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
	manifest, err := manifests.Load(operationID)
	if err != nil || len(manifest.Archives) != 1 || manifest.Archives[0].RepoID != repoID || !manifest.AdminGraceUntil.Equal(manifest.DownloadUntil.Add(10*24*time.Hour)) {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	if _, err := keys.FindByPublicKey(record.Request.RecoveryPublicKey, manifest.DownloadUntil.Add(-time.Second)); err != nil {
		t.Fatalf("bound recovery key: %v", err)
	}
	if removed, err := repoworker.ReapDeletionArchives(archiveRoot, manifest.DownloadUntil); err != nil || removed != 0 {
		t.Fatalf("download expiry removed grace archive: removed=%d err=%v", removed, err)
	}
	if removed, err := repoworker.ReapDeletionArchives(archiveRoot, manifest.AdminGraceUntil); err != nil || removed != 1 {
		t.Fatalf("grace expiry did not remove archive: removed=%d err=%v", removed, err)
	}
}
