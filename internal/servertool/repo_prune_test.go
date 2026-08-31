package servertool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/deploy"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"

	"github.com/google/uuid"
)

// writeRepoPruneFixtureConfig builds a minimal, real server.json with
// repositories.root/results_root pointed at fresh temp directories, enough
// for openFiles(needRepositoryData: true, needRepoResults: true) to succeed.
func writeRepoPruneFixtureConfig(t *testing.T) (configPath, resultsRoot, repositoriesRoot string) {
	t.Helper()
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	svnserve, err := exec.LookPath("svnserve")
	if err != nil {
		t.Skip("svnserve unavailable")
	}
	svnlook, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook unavailable")
	}
	base := t.TempDir()
	root := filepath.Join(base, "service")
	if err := onboarding.Initialize(root); err != nil {
		t.Fatal(err)
	}
	pepperPath := filepath.Join(base, "pepper")
	if err := os.WriteFile(pepperPath, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerPublicPath := filepath.Join(base, "worker_ed25519.pub")
	workerPublic, err := deploy.BootstrapAuthorizedKey()
	if err != nil || os.WriteFile(workerPublicPath, []byte(workerPublic+"\n"), 0o644) != nil {
		t.Fatal("write worker public key")
	}
	resultsRoot = filepath.Join(base, "results")
	repositoriesRoot = filepath.Join(base, "repositories")
	if err := os.MkdirAll(filepath.Join(resultsRoot, "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repositoriesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	serviceRepository := filepath.Join(base, "service-repository")
	serviceWC := filepath.Join(base, "service-wc")
	runRepoPruneCommand(t, svnadmin, "create", serviceRepository)
	runRepoPruneCommand(t, svn, "checkout", "file://"+filepath.ToSlash(serviceRepository), serviceWC)
	activationRoot := filepath.Join(base, "activation")
	if err := os.MkdirAll(activationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	authorizedKeys := filepath.Join(activationRoot, "authorized_keys")
	serviceAuthz := filepath.Join(activationRoot, "service.authz")
	dataAuthz := filepath.Join(base, "repositories.authz")
	for _, path := range []string{authorizedKeys, serviceAuthz, dataAuthz} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath = filepath.Join(base, "server.json")
	configJSON := `{
  "schema":"filees.server-toolchain/v1",
  "root":` + quote(root) + `,
  "otp_pepper_file":` + quote(pepperPath) + `,
  "worker_public_key_file":` + quote(workerPublicPath) + `,
  "operation_ttl":"30m",
  "otp_attempts":3,
  "reverse_port_first":42000,
  "reverse_port_last":42010,
  "repositories":{
    "root":` + quote(repositoriesRoot) + `,
    "results_root":` + quote(resultsRoot) + `,
    "data_authz_file":` + quote(dataAuthz) + `,
    "svnadmin_binary":` + quote(svnadmin) + `,
    "svnlook_binary":` + quote(svnlook) + `,
    "url_prefix":"svn+ssh://_filees-data@filees.test/"
  },
  "activation":{
    "root":` + quote(activationRoot) + `,
    "authorized_keys_file":` + quote(authorizedKeys) + `,
    "authz_file":` + quote(serviceAuthz) + `,
    "service_working_copy":` + quote(serviceWC) + `,
    "service_repository":` + quote(serviceRepository) + `,
    "repository_name":"filees-service",
    "client_entry_path":"/usr/local/libexec/filees/filees-client-entry",
    "svn_binary":` + quote(svn) + `,
    "svnserve_binary":` + quote(svnserve) + `
  },
  "invitation":{"server_id":"office","server_address":"filees.test:2222","known_host":"[filees.test]:2222 ` + workerPublic + `"},
  "smtp":{"address":"127.0.0.1:2525","client_name":"filees.test","from":"filees@example.test","message_id_domain":"filees.test","tls":"none"}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, resultsRoot, repositoriesRoot
}

func runRepoPruneCommand(t *testing.T, command string, args ...string) {
	t.Helper()
	if output, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", command, args, err, output)
	}
}

func writeBackendRecordFixture(t *testing.T, resultsRoot, operationID, realmID, repoID, stage string) {
	t.Helper()
	record := map[string]any{
		"operation_id": operationID, "realm_id": realmID, "name": "AKTUALNE",
		"repo_id": repoID, "url": "svn+ssh://_filees-data@filees.test/" + repoID, "stage": stage,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultsRoot, "backend", operationID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRepoCheckStateAndPruneOnlyTouchBookkeepingOnlyRecords is a regression
// test for the cloud.atmprojekt.pl 2026-08-31 incident
// (concepts/ORPHANED_REPO_CLEANUP_CONCEPT.md): repeated failed "Dodaj folder
// do FileES" attempts before r667 left stuck backend records behind.
// A published record with missing canonical authority is reported for manual
// attention but is not prunable; "allocated" and "rolled_back" have no real
// FSFS content and are prunable; "fsfs_created" has a real, unpublished FSFS
// directory behind it and must never be silently deleted with --apply.
func TestRepoCheckStateAndPruneOnlyTouchBookkeepingOnlyRecords(t *testing.T) {
	configPath, resultsRoot, repositoriesRoot := writeRepoPruneFixtureConfig(t)
	realmID := "8f14e45f-ceea-467e-adde-3fb5787cd831"
	published := "11111111-1111-4111-8111-111111111111"
	allocated := "22222222-2222-4222-8222-222222222222"
	rolledBack := "33333333-3333-4333-8333-333333333333"
	fsfsCreated := "44444444-4444-4444-8444-444444444444"
	publishedRepo := "11111111-aaaa-4111-8111-111111111111"
	allocatedRepo := "22222222-aaaa-4222-8222-222222222222"
	rolledBackRepo := "33333333-aaaa-4333-8333-333333333333"
	fsfsCreatedRepo := "44444444-aaaa-4444-8444-444444444444"
	writeBackendRecordFixture(t, resultsRoot, published, realmID, publishedRepo, "published")
	writeBackendRecordFixture(t, resultsRoot, allocated, realmID, allocatedRepo, "allocated")
	writeBackendRecordFixture(t, resultsRoot, rolledBack, realmID, rolledBackRepo, "rolled_back")
	writeBackendRecordFixture(t, resultsRoot, fsfsCreated, realmID, fsfsCreatedRepo, "fsfs_created")
	// A real, unpublished FSFS directory behind the fsfs_created record -
	// exactly the case that must survive --apply untouched.
	if err := os.MkdirAll(filepath.Join(repositoriesRoot, fsfsCreatedRepo), 0o700); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := RunAdmin([]string{"-config", configPath, "repo", "check-state"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("check-state exit=%d stderr=%s", code, stderr.String())
	}
	var checkResult struct {
		Records []backendRecordReport `json:"records"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &checkResult); err != nil {
		t.Fatalf("decode check-state output: %v: %s", err, stdout.String())
	}
	byOp := map[string]backendRecordReport{}
	for _, record := range checkResult.Records {
		byOp[record.OperationID] = record
	}
	if got := byOp[published]; got.Prunable || got.CanonicalState != "" {
		t.Fatalf("inconsistent published record = %+v, want reported but not prunable", got)
	}
	if got := byOp[allocated]; !got.Prunable || got.FSFSPresent {
		t.Fatalf("allocated record = %+v, want prunable and no FSFS", got)
	}
	if got := byOp[rolledBack]; !got.Prunable || got.FSFSPresent {
		t.Fatalf("rolled_back record = %+v, want prunable and no FSFS", got)
	}
	if got := byOp[fsfsCreated]; got.Prunable || !got.FSFSPresent {
		t.Fatalf("fsfs_created record = %+v, want NOT prunable and FSFS present", got)
	}

	// Dry run (no --apply): nothing on disk changes.
	stdout.Reset()
	stderr.Reset()
	if code := RunAdmin([]string{"-config", configPath, "repo", "prune", "-older-than", "0s"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("prune dry-run exit=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"dry_run"`)) {
		t.Fatalf("prune without --apply was not a dry run: %s", stdout.String())
	}
	for _, id := range []string{published, allocated, rolledBack, fsfsCreated} {
		if _, err := os.Stat(filepath.Join(resultsRoot, "backend", id+".json")); err != nil {
			t.Fatalf("dry-run prune removed %s: %v", id, err)
		}
	}

	// Apply: only allocated and rolled_back are removed.
	stdout.Reset()
	stderr.Reset()
	if code := RunAdmin([]string{"-config", configPath, "repo", "prune", "-older-than", "0s", "-apply"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("prune --apply exit=%d stderr=%s", code, stderr.String())
	}
	for _, id := range []string{allocated, rolledBack} {
		if _, err := os.Stat(filepath.Join(resultsRoot, "backend", id+".json")); !os.IsNotExist(err) {
			t.Fatalf("prune --apply did not remove %s: err=%v", id, err)
		}
	}
	for _, id := range []string{published, fsfsCreated} {
		if _, err := os.Stat(filepath.Join(resultsRoot, "backend", id+".json")); err != nil {
			t.Fatalf("prune --apply removed a record it must not touch (%s): %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repositoriesRoot, fsfsCreatedRepo)); err != nil {
		t.Fatalf("prune --apply must never touch real FSFS content: %v", err)
	}
}

// TestRepoPruneRespectsOlderThan confirms a very fresh, still-in-progress
// attempt (matching the file's mtime, not an explicit timestamp field the
// backend record does not carry) is never pruned even if its stage would
// otherwise qualify - it might just be a live STORAGE_PREFLIGHT/
// CREATE_REPOSITORY exchange that has not reached "published" yet.
func TestRepoPruneRespectsOlderThan(t *testing.T) {
	configPath, resultsRoot, _ := writeRepoPruneFixtureConfig(t)
	realmID := "8f14e45f-ceea-467e-adde-3fb5787cd831"
	fresh := "55555555-5555-4555-8555-555555555555"
	writeBackendRecordFixture(t, resultsRoot, fresh, realmID, "55555555-aaaa-4555-8555-555555555555", "allocated")

	var stdout, stderr bytes.Buffer
	if code := RunAdmin([]string{"-config", configPath, "repo", "prune", "-apply"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("prune --apply exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(resultsRoot, "backend", fresh+".json")); err != nil {
		t.Fatalf("prune removed a fresh record younger than the default --older-than: %v", err)
	}
}

// TestRepoPruneWithdrawsOnlyEmptyInitializingPublishedRepository captures the
// visible cloud.atmprojekt.pl failure: CREATE_REPOSITORY durably projected
// three entries named AKTUALNE, but their initial import never completed.
// An initializing r0 repository is removed from every client view, authz and
// physical storage. An active r0 repository, an initializing repository with
// even one committed revision, and an ordinary deletion tombstone are all
// preserved. Only a durable prune_pending marker may resume from deleted.
func TestRepoPruneWithdrawsOnlyEmptyInitializingPublishedRepository(t *testing.T) {
	configPath, resultsRoot, repositoriesRoot := writeRepoPruneFixtureConfig(t)
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		t.Fatal(err)
	}
	realmID, clientID := uuid.NewString(), uuid.NewString()
	viewPath := filepath.Join(config.Activation.ServiceWorkingCopy, "clients", clientID, "view.json")
	view := clientview.View{
		Schema: clientview.Schema, ClientID: clientID, RealmID: realmID,
		Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal",
		Capabilities: &clientview.Capabilities{CanCreateRepositories: true},
		Repositories: []clientview.Repository{}, ActiveOperations: []json.RawMessage{},
	}
	if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
		t.Fatal(err)
	}
	clientPath := filepath.Join(config.Activation.ServiceWorkingCopy, "admin", "clients", clientID+".json")
	realmPath := filepath.Join(config.Activation.ServiceWorkingCopy, "admin", "realms", realmID+".json")
	writeRepoPruneJSON(t, clientPath, map[string]any{
		"schema": "filees.client-instance/v1", "client_id": clientID, "realm_id": realmID, "kind": "desktop", "state": "active",
	})
	writeRepoPruneJSON(t, realmPath, map[string]any{
		"schema": "filees.realm/v1", "realm_id": realmID, "state": "active", "created_at": time.Now().UTC(), "alias": "pracownia",
	})
	runner := repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: config.Activation.ServiceWorkingCopy}
	if err := runner.Publish(context.Background(), []string{viewPath, clientPath, realmPath}, "seed prune authority fixture"); err != nil {
		t.Fatal(err)
	}
	publisher := repoworker.ServicePublisher{
		ServiceWC: config.Activation.ServiceWorkingCopy, DataAuthzFile: config.Repositories.DataAuthzFile,
		Runner: runner,
	}
	type repositoryCase struct {
		operationID string
		repoID      string
		name        string
	}
	ghost := repositoryCase{uuid.NewString(), uuid.NewString(), "AKTUALNE-duch"}
	active := repositoryCase{uuid.NewString(), uuid.NewString(), "AKTUALNE-aktywne"}
	committed := repositoryCase{uuid.NewString(), uuid.NewString(), "AKTUALNE-z-danymi"}
	normalDeleted := repositoryCase{uuid.NewString(), uuid.NewString(), "AKTUALNE-zwykle-kasowane"}
	pruneRetry := repositoryCase{uuid.NewString(), uuid.NewString(), "AKTUALNE-przerwany-prune"}
	for _, item := range []repositoryCase{ghost, active, committed, normalDeleted, pruneRetry} {
		url := "svn+ssh://_filees-data@filees.test/" + item.repoID
		if err := publisher.Publish(context.Background(), item.repoID, realmID, item.name, url, ""); err != nil {
			t.Fatalf("publish %s: %v", item.name, err)
		}
		runRepoPruneCommand(t, config.Repositories.SVNAdminBinary, "create", filepath.Join(repositoriesRoot, item.repoID))
		writeBackendRecordFixture(t, resultsRoot, item.operationID, realmID, item.repoID, "published")
	}
	if err := publisher.Activate(context.Background(), active.repoID, realmID); err != nil {
		t.Fatal(err)
	}
	runRepoPruneCommand(t, config.Activation.SVNBinary, "mkdir", "file://"+filepath.ToSlash(filepath.Join(repositoriesRoot, committed.repoID))+"/real-data", "-m", "real initial content")
	for _, item := range []repositoryCase{normalDeleted, pruneRetry} {
		if err := publisher.Delete(context.Background(), item.repoID, realmID); err != nil {
			t.Fatalf("delete fixture %s: %v", item.name, err)
		}
	}
	writeBackendRecordFixture(t, resultsRoot, pruneRetry.operationID, realmID, pruneRetry.repoID, "prune_pending")

	var stdout, stderr bytes.Buffer
	if code := RunAdmin([]string{"-config", configPath, "repo", "check-state"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("check-state exit=%d stderr=%s", code, stderr.String())
	}
	var check struct {
		Records []backendRecordReport `json:"records"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &check); err != nil {
		t.Fatal(err)
	}
	byRepo := map[string]backendRecordReport{}
	for _, record := range check.Records {
		byRepo[record.RepoID] = record
	}
	if got := byRepo[ghost.repoID]; !got.Prunable || got.CanonicalState != "initializing" || got.YoungestRevision == nil || *got.YoungestRevision != 0 {
		t.Fatalf("empty initializing ghost = %+v, want prunable r0", got)
	}
	if _, reported := byRepo[active.repoID]; reported {
		t.Fatalf("normal active repository was reported: %+v", byRepo[active.repoID])
	}
	if got := byRepo[committed.repoID]; got.Prunable || got.YoungestRevision == nil || *got.YoungestRevision != 1 {
		t.Fatalf("initializing repository with data = %+v, want protected r1", got)
	}
	if got := byRepo[normalDeleted.repoID]; got.Prunable || got.CanonicalState != "deleted" || got.YoungestRevision != nil {
		t.Fatalf("ordinary deletion tombstone = %+v, want protected without r0 inference", got)
	}
	if got := byRepo[pruneRetry.repoID]; !got.Prunable || got.CanonicalState != "deleted" || got.YoungestRevision == nil || *got.YoungestRevision != 0 {
		t.Fatalf("interrupted prune = %+v, want resumable deleted r0", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := RunAdmin([]string{"-config", configPath, "repo", "prune", "-older-than", "0s", "-apply"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("prune --apply exit=%d stderr=%s", code, stderr.String())
	}
	for _, item := range []repositoryCase{ghost, pruneRetry} {
		if _, err := os.Stat(filepath.Join(resultsRoot, "backend", item.operationID+".json")); !os.IsNotExist(err) {
			t.Fatalf("pruned backend %s survived: %v", item.name, err)
		}
		if _, err := os.Stat(filepath.Join(repositoriesRoot, item.repoID)); !os.IsNotExist(err) {
			t.Fatalf("pruned empty FSFS %s survived: %v", item.name, err)
		}
	}
	for _, item := range []repositoryCase{active, committed, normalDeleted} {
		if _, err := os.Stat(filepath.Join(resultsRoot, "backend", item.operationID+".json")); err != nil {
			t.Fatalf("protected backend %s was removed: %v", item.name, err)
		}
		if _, err := os.Stat(filepath.Join(repositoriesRoot, item.repoID)); err != nil {
			t.Fatalf("protected FSFS %s was removed: %v", item.name, err)
		}
	}
	updated, err := clientview.Load(viewPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range updated.Repositories {
		if repository.RepoID == ghost.repoID {
			t.Fatalf("ghost remained in client projection: %+v", updated.Repositories)
		}
	}
	if len(updated.Repositories) != 2 {
		t.Fatalf("projection after prune = %+v, want two protected repositories", updated.Repositories)
	}
	var canonical struct {
		State string `json:"state"`
	}
	raw, err := os.ReadFile(filepath.Join(config.Activation.ServiceWorkingCopy, "admin", "repositories", ghost.repoID+".json"))
	if err != nil || json.Unmarshal(raw, &canonical) != nil || canonical.State != "deleted" {
		t.Fatalf("ghost canonical tombstone state=%q err=%v raw=%s", canonical.State, err, raw)
	}
}

func writeRepoPruneJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
