package servertool

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestRepositoryRecoveryPublisherBindsCompletedDeletionArchive(t *testing.T) {
	root := t.TempDir()
	backendRoot := filepath.Join(root, "backend")
	archiveRoot := filepath.Join(root, "deleted-repositories")
	resultsRoot := filepath.Join(root, "results")
	for _, dir := range []string{backendRoot, archiveRoot, resultsRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	operationID, realmID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(48 * time.Hour)
	writeJSON := func(path string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(filepath.Join(backendRoot, "delete-"+operationID+".json"), map[string]any{
		"operation_id": operationID, "realm_id": realmID, "repo_id": repoID,
		"stage": "deleted", "retain_until": deadline.Format(time.RFC3339Nano),
	})
	dump := []byte("SVN-fs-dump-format-version: 2\n")
	base := repoID + "-" + operationID
	if err := os.WriteFile(filepath.Join(archiveRoot, base+".svndump"), dump, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(dump)
	writeJSON(filepath.Join(archiveRoot, base+".json"), map[string]any{
		"schema": "filees.deleted-repository/v1", "operation_id": operationID,
		"repo_id": repoID, "dump_file": base + ".svndump",
		"sha256": hex.EncodeToString(digest[:]), "created_at": now, "delete_after": deadline,
	})

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(privateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	publicKey := string(ssh.MarshalAuthorizedKey(public))
	publisher := repositoryRecoveryPublisher{
		Backend: &repoworker.DurableBackend{Root: backendRoot}, ArchiveRoot: archiveRoot,
		Manifests: repoworker.RecoveryManifestStore{Root: filepath.Join(resultsRoot, "recovery-manifests")},
		Keys:      repoworker.RecoveryKeyStore{Root: filepath.Join(resultsRoot, "recovery-keys")},
		Now:       func() time.Time { return now },
	}
	manifest, err := publisher.Prepare(context.Background(), repoworker.Session{ClientID: "client", RealmID: realmID, CanCreateRepositories: true}, operationID, repoID, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.OperationID != operationID || manifest.RealmID != realmID || manifest.DownloadUntil != deadline || len(manifest.Archives) != 1 || manifest.Archives[0].RepoID != repoID {
		t.Fatalf("manifest=%+v", manifest)
	}
	if _, err := publisher.Keys.FindByPublicKey(publicKey, now); err != nil {
		t.Fatalf("bound recovery key unavailable: %v", err)
	}
	// The same receipt is immutable and replay-safe.
	replayed, err := publisher.Prepare(context.Background(), repoworker.Session{ClientID: "client", RealmID: realmID, CanCreateRepositories: true}, operationID, repoID, publicKey)
	if err != nil || replayed.OperationID != manifest.OperationID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}
