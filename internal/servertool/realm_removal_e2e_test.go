package servertool

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"filees/pkg/activation"
	"filees/pkg/clientview"
	"filees/pkg/onboarding"
	"filees/pkg/recoverykit"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// TestRealmRemovalDestructiveE2E uses real SVN repositories, the real service
// publisher, durable delete backend, activation manager, OTP journal, recovery
// publisher, recovery kit and erasure journal. The whole fixture is rooted in
// t.TempDir so the same binary can run natively on OpenBSD without touching an
// installed FileES instance.
func TestRealmRemovalDestructiveE2E(t *testing.T) {
	for _, retentionDays := range []int{0, 2} {
		retentionDays := retentionDays
		t.Run("retention_"+strconv.Itoa(retentionDays), func(t *testing.T) {
			f := newRealmRemovalE2EFixture(t, retentionDays)
			f.run(t)
		})
	}
}

// TestRealmRemovalRecoveryForcedCommandHelper is entered only by the E2E
// subprocess. runRecoveryEntry permanently narrows pledge/unveil on OpenBSD,
// so it must not run in the parent test process that still owns temp cleanup.
func TestRealmRemovalRecoveryForcedCommandHelper(t *testing.T) {
	if os.Getenv("FILEES_REALM_RECOVERY_HELPER") != "1" {
		t.Skip("realm removal recovery helper")
	}
	getenv := func(name string) string {
		if name == "SSH_ORIGINAL_COMMAND" {
			return RecoveryCommand
		}
		return ""
	}
	code := runRecoveryEntry(
		os.Getenv("FILEES_REALM_RECOVERY_CONFIG"),
		[]string{"serve", os.Getenv("FILEES_REALM_RECOVERY_OPERATION")},
		os.Stdin, os.Stdout, os.Stderr, getenv, time.Now,
	)
	os.Exit(code)
}

type realmRemovalE2EFixture struct {
	root, repositoriesRoot, resultsRoot, archiveRoot string
	svn, svnadmin                                    string
	now                                              time.Time
	retentionDays                                    int
	targetRealm, otherRealm                          string
	targetClients                                    []onboarding.ActivationGrant
	otherClient                                      onboarding.ActivationGrant
	ownedRepos, foreignRepos                         []repoworker.Repository
	manager                                          *activation.Manager
	activationConfig                                 activation.Config
	publisher                                        repoworker.ServicePublisher
	backend                                          *repoworker.DurableBackend
}

func newRealmRemovalE2EFixture(t *testing.T, retentionDays int) realmRemovalE2EFixture {
	t.Helper()
	find := func(name string) string {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s unavailable", name)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	f := realmRemovalE2EFixture{
		root: t.TempDir(), svn: find("svn"), svnadmin: find("svnadmin"),
		now: time.Now().UTC().Truncate(time.Second), retentionDays: retentionDays,
		targetRealm: uuid.NewString(), otherRealm: uuid.NewString(),
	}
	svnserve := find("svnserve")
	serviceRepository := filepath.Join(f.root, "service-repository")
	serviceWC := filepath.Join(f.root, "service-wc")
	realmRemovalE2ERun(t, f.svnadmin, "create", serviceRepository)
	realmRemovalE2ERun(t, f.svn, "mkdir", "--non-interactive", "--no-auth-cache", "-m", "initialize proof", "file://"+serviceRepository+"/proof")
	realmRemovalE2ERun(t, f.svn, "checkout", "--non-interactive", "--no-auth-cache", "file://"+serviceRepository, serviceWC)

	f.repositoriesRoot = filepath.Join(f.root, "repositories")
	f.resultsRoot = filepath.Join(f.root, "results")
	f.archiveRoot = filepath.Join(f.resultsRoot, "deleted-repositories")
	f.activationConfig = activation.Config{
		Root: filepath.Join(f.root, "activation"), SessionRoot: filepath.Join(f.root, "sessions"),
		AuthorizedKeysFile: filepath.Join(f.root, "authorized_keys"), AuthzFile: filepath.Join(f.root, "service.authz"),
		ServiceWorkingCopy: serviceWC, ServiceRepository: serviceRepository, RepositoryName: "filees-service",
		ClientEntryPath: "/usr/local/libexec/filees/filees-client-entry", SVNBinary: f.svn, SVNServeBinary: svnserve,
	}
	manager, err := activation.New(f.activationConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.manager = manager
	f.publisher = repoworker.ServicePublisher{
		ServiceWC: serviceWC, DataAuthzFile: filepath.Join(f.root, "repositories.authz"),
		Runner: repoworker.SVNPublishRunner{SVN: f.svn, WorkingCopy: serviceWC}, Now: func() time.Time { return f.now },
	}
	effects := repoworker.ServerEffects{
		SVNAdmin: f.svnadmin, RepositoriesRoot: f.repositoriesRoot, DataAuthzFile: f.publisher.DataAuthzFile,
		DeletionArchiveRoot: f.archiveRoot, DeletionRetentionDays: retentionDays,
		Authority: f.publisher, Now: func() time.Time { return f.now },
	}
	f.backend = &repoworker.DurableBackend{
		Root: filepath.Join(f.resultsRoot, "backend"), URLPrefix: "svn+ssh://_filees-client@realm-e2e.test/", Effects: effects,
	}

	f.targetClients = []onboarding.ActivationGrant{
		f.activate(t, f.targetRealm), f.activate(t, f.targetRealm),
	}
	f.otherClient = f.activate(t, f.otherRealm)
	f.foreignRepos = []repoworker.Repository{
		f.createRepository(t, f.otherRealm, "foreign-read"),
		f.createRepository(t, f.otherRealm, "foreign-write"),
	}
	f.grantForeignRepositories(t)
	f.ownedRepos = []repoworker.Repository{
		f.createRepository(t, f.targetRealm, "owned-one"),
		f.createRepository(t, f.targetRealm, "owned-two"),
	}
	return f
}

func (f *realmRemovalE2EFixture) activate(t *testing.T, realmID string) onboarding.ActivationGrant {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	grant := onboarding.ActivationGrant{
		OperationID: uuid.NewString(), ClientID: uuid.NewString(), RealmID: realmID, DeployRequestID: uuid.NewString(),
		InstallationPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " realm-removal-e2e",
		InstallationFingerprint: ssh.FingerprintSHA256(key), ExpiresAt: f.now.Add(time.Hour),
	}
	if err := f.manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.Publish(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	return grant
}

func (f *realmRemovalE2EFixture) createRepository(t *testing.T, realmID, name string) repoworker.Repository {
	t.Helper()
	repository, err := f.backend.Create(context.Background(), uuid.NewString(), realmID, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.publisher.Activate(context.Background(), repository.RepoID, realmID); err != nil {
		t.Fatal(err)
	}
	realmRemovalE2ERun(t, f.svn, "mkdir", "--non-interactive", "--no-auth-cache", "-m", "seed "+name, "file://"+filepath.Join(f.repositoriesRoot, repository.RepoID)+"/data")
	return repository
}

func (f *realmRemovalE2EFixture) grantForeignRepositories(t *testing.T) {
	t.Helper()
	paths := make([]string, 0, len(f.targetClients))
	for _, client := range f.targetClients {
		path := filepath.Join(f.activationConfig.ServiceWorkingCopy, "clients", client.ClientID, "view.json")
		view, err := clientview.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		view.Generation++
		view.GeneratedAt = f.now
		for index, repository := range f.foreignRepos {
			access := "r"
			if index == 1 {
				access = "rw"
			}
			view.Repositories = append(view.Repositories, clientview.Repository{
				RepoID: repository.RepoID, DisplayName: "foreign", URL: repository.URL,
				Access: access, State: "active", OwnerRealmID: f.otherRealm, AttachmentPolicy: "optional",
			})
		}
		if _, err := clientview.StoreIfNewer(path, view); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := (repoworker.SVNPublishRunner{SVN: f.svn, WorkingCopy: f.activationConfig.ServiceWorkingCopy}).Publish(context.Background(), paths, "realm e2e: grant foreign repositories"); err != nil {
		t.Fatal(err)
	}
}

func (f *realmRemovalE2EFixture) run(t *testing.T) {
	t.Helper()
	lease, err := f.manager.ClaimSession(f.targetClients[0].OperationID, f.targetClients[0].ClientID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	store := repoworker.RealmRemovalStore{
		Root: filepath.Join(f.resultsRoot, "realm-removals"), OTPPepper: bytes.Repeat([]byte{0x5a}, 32),
		TTL: time.Hour, Attempts: 3, Now: func() time.Time { return f.now },
	}
	manifests := repoworker.RecoveryManifestStore{Root: filepath.Join(f.resultsRoot, "recovery-manifests")}
	keys := repoworker.RecoveryKeyStore{Root: filepath.Join(f.resultsRoot, "recovery-keys")}
	erasure := repoworker.DataErasureStore{Root: filepath.Join(f.resultsRoot, "data-erasure")}
	recovery := realmRecoveryPublisher{ArchiveRoot: f.archiveRoot, Manifests: manifests, Keys: keys, Grace: 24 * time.Hour}
	executor := realmRemovalExecutor{
		Store: store, Backend: f.backend, Recovery: recovery, Publisher: f.publisher, Activation: f.manager,
		Erasure: erasure, ErasureMaxDays: 90,
	}
	coordinator := realmRemovalCoordinator{
		Store: store, SnapshotScope: f.publisher.SnapshotRealmScope, ActiveClients: f.manager.ActiveClientsInRealm,
		Execute: executor.Execute, Manifests: manifests,
	}
	operationID := uuid.NewString()
	hostKey := strings.TrimSpace(testRecoveryPublicKey(t))
	draft, publicKey, err := recoverykit.CreateDraft("127.0.0.1:2222", "[127.0.0.1]:2222 "+hostKey, operationID, f.targetRealm)
	if err != nil {
		t.Fatal(err)
	}
	session := repoworker.Session{ClientID: f.targetClients[0].ClientID, RealmID: f.targetRealm}
	record, err := coordinator.Request(context.Background(), session, operationID, repoworker.RealmRemovalRequest{
		NotificationEmail: "realm-e2e@example.test", ErasureRequested: true, RecoveryPublicKey: publicKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Scope.ClientIDs) != 2 || len(record.Scope.OwnedRepoIDs) != 2 || len(record.Scope.ForeignGrantRepoIDs) != 2 {
		t.Fatalf("unexpected removal snapshot: %+v", record.Scope)
	}
	job, err := store.ClaimPendingMail(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkMailQueued(job.OperationID, job.AttemptID); err != nil {
		t.Fatal(err)
	}
	record, manifest, err := coordinator.Confirm(context.Background(), session, operationID, job.OTP)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != repoworker.RealmRemovalCompleted || manifest.OperationID != operationID || manifest.RealmID != f.targetRealm {
		t.Fatalf("completed record=%+v manifest=%+v", record, manifest)
	}

	kit, err := recoverykit.Finalize(draft, manifest)
	if err != nil {
		t.Fatal(err)
	}
	kitPath := filepath.Join(f.root, "recovery", operationID+".fkr")
	if err := recoverykit.Store(kitPath, kit); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverykit.Load(kitPath, f.now); err != nil {
		t.Fatal(err)
	}

	f.assertAuthorityAndRevocation(t, lease)
	f.assertRecoveryAndErasure(t, operationID, publicKey, manifest)
	f.assertNoOutboxSecret(t, operationID, job.OTP)
}

func (f *realmRemovalE2EFixture) assertAuthorityAndRevocation(t *testing.T, lease *activation.SessionLease) {
	t.Helper()
	if revoked, err := lease.Revoked(); err != nil || !revoked {
		t.Fatalf("live session revoked=%v err=%v", revoked, err)
	}
	if clients, err := f.manager.ActiveClientsInRealm(f.targetRealm); err != nil || len(clients) != 0 {
		t.Fatalf("removed realm active clients=%v err=%v", clients, err)
	}
	if clients, err := f.manager.ActiveClientsInRealm(f.otherRealm); err != nil || len(clients) != 1 || clients[0] != f.otherClient.ClientID {
		t.Fatalf("foreign realm active clients=%v err=%v", clients, err)
	}
	keysRaw, err := os.ReadFile(f.activationConfig.AuthorizedKeysFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range f.targetClients {
		if bytes.Contains(keysRaw, []byte(client.ClientID)) {
			t.Fatalf("revoked client remains authorized: %s", client.ClientID)
		}
		view, err := clientview.Load(filepath.Join(f.activationConfig.ServiceWorkingCopy, "clients", client.ClientID, "view.json"))
		if err != nil || len(view.Repositories) != 0 {
			t.Fatalf("removed client view=%+v err=%v", view.Repositories, err)
		}
	}
	if !bytes.Contains(keysRaw, []byte(f.otherClient.ClientID)) {
		t.Fatal("foreign realm client was revoked")
	}
	for _, repository := range f.ownedRepos {
		if _, err := os.Lstat(filepath.Join(f.repositoriesRoot, repository.RepoID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned FSFS survived: %s err=%v", repository.RepoID, err)
		}
		raw, err := os.ReadFile(filepath.Join(f.activationConfig.ServiceWorkingCopy, "admin", "repositories", repository.RepoID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			State string `json:"state"`
		}
		if json.Unmarshal(raw, &record) != nil || record.State != "deleted" {
			t.Fatalf("owned repository tombstone=%s", raw)
		}
	}
	for _, repository := range f.foreignRepos {
		if _, err := os.Stat(filepath.Join(f.repositoriesRoot, repository.RepoID, "format")); err != nil {
			t.Fatalf("foreign FSFS removed: %s err=%v", repository.RepoID, err)
		}
	}
	authz, err := os.ReadFile(f.publisher.DataAuthzFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range f.ownedRepos {
		if bytes.Contains(authz, []byte(repository.RepoID)) {
			t.Fatalf("deleted repository remains in authz: %s", repository.RepoID)
		}
	}
}

func (f *realmRemovalE2EFixture) assertRecoveryAndErasure(t *testing.T, operationID, publicKey string, manifest repoworker.RecoveryManifest) {
	t.Helper()
	erasure, err := (repoworker.DataErasureStore{Root: filepath.Join(f.resultsRoot, "data-erasure")}).Load(operationID)
	if err != nil || erasure.State != repoworker.DataErasureAwaitingBackupRetention || erasure.ActiveDataDeletedAt == nil {
		t.Fatalf("erasure=%+v err=%v", erasure, err)
	}
	keys := repoworker.RecoveryKeyStore{Root: filepath.Join(f.resultsRoot, "recovery-keys")}
	if f.retentionDays == 0 {
		if len(manifest.Archives) != 0 {
			t.Fatalf("zero retention published archives: %+v", manifest.Archives)
		}
		if _, err := keys.FindByPublicKey(publicKey, f.now); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("zero retention recovery key exists: %v", err)
		}
		return
	}
	if len(manifest.Archives) != len(f.ownedRepos) {
		t.Fatalf("retained archives=%d want=%d", len(manifest.Archives), len(f.ownedRepos))
	}
	if _, err := keys.FindByPublicKey(publicKey, f.now); err != nil {
		t.Fatalf("recovery key unavailable: %v", err)
	}
	archive := manifest.Archives[0]
	request := "get " + operationID + " " + archive.ArchiveID + "\n"
	configPath := filepath.Join(f.root, "recovery-server.json")
	config := map[string]any{
		"schema": serverconfig.Schema, "root": filepath.Join(f.root, "onboarding"),
		"otp_pepper_file": filepath.Join(f.root, "pepper"), "operation_ttl": "30m",
		"otp_attempts": 3, "reverse_port_first": 42000, "reverse_port_last": 42000,
		"repositories": map[string]any{
			"root": f.repositoriesRoot, "results_root": f.resultsRoot,
			"deletion_archive_root": f.archiveRoot,
		},
		"smtp": map[string]any{
			"address": "127.0.0.1:2525", "client_name": "realm-e2e.test",
			"from": "filees@realm-e2e.test", "message_id_domain": "realm-e2e.test", "tls": "none",
		},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var downloaded, stderr bytes.Buffer
	command := exec.Command(os.Args[0], "-test.run=^TestRealmRemovalRecoveryForcedCommandHelper$")
	command.Env = append(os.Environ(),
		"FILEES_REALM_RECOVERY_HELPER=1",
		"FILEES_REALM_RECOVERY_CONFIG="+configPath,
		"FILEES_REALM_RECOVERY_OPERATION="+operationID,
	)
	command.Stdin, command.Stdout, command.Stderr = strings.NewReader(request), &downloaded, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("recovery forced command: %v stderr=%s", err, stderr.String())
	}
	deleteOperationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":"+archive.RepoID+":delete")).String()
	want, err := os.ReadFile(filepath.Join(f.archiveRoot, archive.RepoID+"-"+deleteOperationID+".svndump"))
	if err != nil || !bytes.Equal(downloaded.Bytes(), want) {
		t.Fatalf("recovery bytes=%d want=%d err=%v", downloaded.Len(), len(want), err)
	}
}

func (f *realmRemovalE2EFixture) assertNoOutboxSecret(t *testing.T, operationID, otp string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.resultsRoot, "realm-removals", "outbox", operationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(otp)) || bytes.Contains(raw, []byte("realm-e2e@example.test")) {
		t.Fatalf("completed outbox retained delivery secret: %s", raw)
	}
}

func realmRemovalE2ERun(t *testing.T, command string, args ...string) {
	t.Helper()
	if output, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", command, args, err, output)
	}
}
