package servertool

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

type recoveryEntryFixture struct {
	configPath  string
	operationID string
	publicKey   string
	manifest    repoworker.RecoveryManifest
	dump        []byte
	now         time.Time
}

func newRecoveryEntryFixture(t *testing.T) recoveryEntryFixture {
	t.Helper()
	svnadmin := requireSVN(t, "svnadmin")[0]
	root := t.TempDir()
	results := filepath.Join(root, "results")
	repositories := filepath.Join(root, "repositories")
	archives := filepath.Join(results, "deleted-repositories")
	if err := os.MkdirAll(repositories, 0o700); err != nil {
		t.Fatal(err)
	}
	repoID, operationID := uuid.NewString(), uuid.NewString()
	if output, err := exec.Command(svnadmin, "create", filepath.Join(repositories, repoID)).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, output)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	deleteOperationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":"+repoID+":delete")).String()
	effects := repoworker.ServerEffects{
		SVNAdmin: svnadmin, RepositoriesRoot: repositories,
		DeletionArchiveRoot: archives, DeletionRetentionDays: 7,
		Now: func() time.Time { return now },
	}
	retainedUntil, err := effects.ArchiveAndDeleteFSFS(context.Background(), repoID, deleteOperationID)
	if err != nil {
		t.Fatal(err)
	}
	archive, _, found, err := repoworker.DeletionRecoveryArchive(archives, repoID, deleteOperationID)
	if err != nil || !found {
		t.Fatalf("archive=%+v found=%v err=%v", archive, found, err)
	}
	manifest := repoworker.RecoveryManifest{
		Schema: repoworker.RecoveryManifestSchema, OperationID: operationID, RealmID: uuid.NewString(),
		Archives: []repoworker.RecoveryArchive{archive}, CreatedAt: now,
		DownloadUntil: retainedUntil, AdminGraceUntil: retainedUntil.Add(10 * 24 * time.Hour),
	}
	manifestStore := repoworker.RecoveryManifestStore{Root: filepath.Join(results, "recovery-manifests")}
	if err := manifestStore.Save(manifest); err != nil {
		t.Fatal(err)
	}
	publicKey := testRecoveryPublicKey(t)
	if _, err := (repoworker.RecoveryKeyStore{Root: filepath.Join(results, "recovery-keys")}).Bind(manifest, publicKey); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "server.json")
	config := map[string]any{
		"schema": serverconfig.Schema, "display_name": "Serwer testowy", "root": filepath.Join(root, "onboarding"),
		"otp_pepper_file": filepath.Join(root, "pepper"), "operation_ttl": "30m",
		"otp_attempts": 3, "reverse_port_first": 42000, "reverse_port_last": 42000,
		"repositories": map[string]any{"root": repositories, "results_root": results, "deletion_archive_root": archives, "url_prefix": "svn+ssh://_filees-data@filees.test/"},
		"invitation": map[string]any{
			"server_id": "recovery-entry-test", "server_address": "filees.test:2222",
			"known_host": "[filees.test]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		"smtp": map[string]any{"address": "127.0.0.1:2525", "client_name": "filees.test", "from": "filees@example.test", "message_id_domain": "filees.test", "tls": "none"},
	}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dump, err := os.ReadFile(filepath.Join(archives, repoID+"-"+deleteOperationID+".svndump"))
	if err != nil {
		t.Fatal(err)
	}
	return recoveryEntryFixture{configPath: configPath, operationID: operationID, publicKey: publicKey, manifest: manifest, dump: dump, now: now}
}

func TestRecoveryEntryAuthorizesBoundKeyAndStreamsExactArchive(t *testing.T) {
	if isolateSandboxingTest(t, "TestRecoveryEntryAuthorizesBoundKeyAndStreamsExactArchive") {
		return
	}
	permitRepeatedSandbox(t)
	f := newRecoveryEntryFixture(t)
	keyFields := strings.Fields(f.publicKey)
	var authorized, stderr bytes.Buffer
	code := runRecoveryEntry(f.configPath, []string{"authorize", keyFields[0], keyFields[1]}, bytes.NewReader(nil), &authorized, &stderr, func(string) string { return "" }, func() time.Time { return f.now })
	if code != ExitOK || !strings.Contains(authorized.String(), "restrict,command=") || !strings.Contains(authorized.String(), f.operationID) {
		t.Fatalf("authorize code=%d out=%q stderr=%s", code, authorized.String(), stderr.String())
	}

	getenv := func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return RecoveryCommand
		}
		return ""
	}
	var listed bytes.Buffer
	code = runRecoveryEntry(f.configPath, []string{"serve", f.operationID}, strings.NewReader("list "+f.operationID+"\n"), &listed, &stderr, getenv, func() time.Time { return f.now })
	if code != ExitOK || !bytes.Contains(listed.Bytes(), []byte(f.manifest.Archives[0].ArchiveID)) {
		t.Fatalf("list code=%d out=%s stderr=%s", code, listed.String(), stderr.String())
	}
	var downloaded bytes.Buffer
	request := "get " + f.operationID + " " + f.manifest.Archives[0].ArchiveID + "\n"
	code = runRecoveryEntry(f.configPath, []string{"serve", f.operationID}, strings.NewReader(request), &downloaded, &stderr, getenv, func() time.Time { return f.now })
	if code != ExitOK || !bytes.Equal(downloaded.Bytes(), f.dump) {
		t.Fatalf("get code=%d bytes=%d want=%d stderr=%s", code, downloaded.Len(), len(f.dump), stderr.String())
	}
}

func TestRecoveryEntryRejectsExpiredAndUnboundCapabilities(t *testing.T) {
	if isolateSandboxingTest(t, "TestRecoveryEntryRejectsExpiredAndUnboundCapabilities") {
		return
	}
	permitRepeatedSandbox(t)
	f := newRecoveryEntryFixture(t)
	keyFields := strings.Fields(f.publicKey)
	var stdout, stderr bytes.Buffer
	expired := f.manifest.DownloadUntil
	if code := runRecoveryEntry(f.configPath, []string{"authorize", keyFields[0], keyFields[1]}, bytes.NewReader(nil), &stdout, &stderr, func(string) string { return "" }, func() time.Time { return expired }); code != ExitUnavailable {
		t.Fatalf("expired key authorized with code %d", code)
	}
	getenv := func(string) string { return RecoveryCommand }
	request := "get " + f.operationID + " " + f.manifest.Archives[0].ArchiveID + "\n"
	if code := runRecoveryEntry(f.configPath, []string{"serve", f.operationID}, strings.NewReader(request), &stdout, &stderr, getenv, func() time.Time { return expired }); code != ExitUnavailable {
		t.Fatalf("expired download served with code %d", code)
	}
	if code := runRecoveryEntry(f.configPath, []string{"serve", f.operationID}, strings.NewReader("get "+f.operationID+" "+uuid.NewString()+"\n"), &stdout, &stderr, getenv, func() time.Time { return f.now }); code != ExitUnavailable {
		t.Fatalf("foreign archive served with code %d", code)
	}
}
