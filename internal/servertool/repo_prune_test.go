package servertool

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/deploy"
	"filees/pkg/onboarding"
)

// writeRepoPruneFixtureConfig builds a minimal, real server.json with
// repositories.root/results_root pointed at fresh temp directories, enough
// for openFiles(needRepositoryData: true, needRepoResults: true) to succeed.
func writeRepoPruneFixtureConfig(t *testing.T) (configPath, resultsRoot, repositoriesRoot string) {
	t.Helper()
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
    "data_authz_file":` + quote(filepath.Join(base, "repositories.authz")) + `,
    "svnadmin_binary":"/bin/sh",
    "url_prefix":"svn+ssh://_filees-data@filees.test/"
  },
  "invitation":{"server_id":"office","server_address":"filees.test:2222","known_host":"[filees.test]:2222 ` + workerPublic + `"},
  "smtp":{"address":"127.0.0.1:2525","client_name":"filees.test","from":"filees@example.test","message_id_domain":"filees.test","tls":"none"}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, resultsRoot, repositoriesRoot
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
// "published" is never even reported (it is the normal, successful case,
// not a cleanup target); "allocated" and "rolled_back" have no real FSFS
// content and are prunable; "fsfs_created" has a real, unpublished FSFS
// directory behind it and must never be silently deleted with --apply.
func TestRepoCheckStateAndPruneOnlyTouchBookkeepingOnlyRecords(t *testing.T) {
	configPath, resultsRoot, repositoriesRoot := writeRepoPruneFixtureConfig(t)
	realmID := "8f14e45f-ceea-467e-adde-3fb5787cd831"
	published := "11111111-1111-4111-8111-111111111111"
	allocated := "22222222-2222-4222-8222-222222222222"
	rolledBack := "33333333-3333-4333-8333-333333333333"
	fsfsCreated := "44444444-4444-4444-8444-444444444444"
	writeBackendRecordFixture(t, resultsRoot, published, realmID, "repo-published", "published")
	writeBackendRecordFixture(t, resultsRoot, allocated, realmID, "repo-allocated", "allocated")
	writeBackendRecordFixture(t, resultsRoot, rolledBack, realmID, "repo-rolled-back", "rolled_back")
	writeBackendRecordFixture(t, resultsRoot, fsfsCreated, realmID, "repo-fsfs-created", "fsfs_created")
	// A real, unpublished FSFS directory behind the fsfs_created record -
	// exactly the case that must survive --apply untouched.
	if err := os.MkdirAll(filepath.Join(repositoriesRoot, "repo-fsfs-created"), 0o700); err != nil {
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
	if _, ok := byOp[published]; ok {
		t.Fatalf("published record was reported, must never be: %+v", checkResult.Records)
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
	if _, err := os.Stat(filepath.Join(repositoriesRoot, "repo-fsfs-created")); err != nil {
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
	writeBackendRecordFixture(t, resultsRoot, fresh, realmID, "repo-fresh", "allocated")

	var stdout, stderr bytes.Buffer
	if code := RunAdmin([]string{"-config", configPath, "repo", "prune", "-apply"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("prune --apply exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(resultsRoot, "backend", fresh+".json")); err != nil {
		t.Fatalf("prune removed a fresh record younger than the default --older-than: %v", err)
	}
}
