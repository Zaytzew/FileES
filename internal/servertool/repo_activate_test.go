package servertool

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func activateFixturePaths(t *testing.T, configPath string) (serviceWC, svnBinary string) {
	t.Helper()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Activation struct {
			ServiceWorkingCopy string `json:"service_working_copy"`
			SVNBinary          string `json:"svn_binary"`
		} `json:"activation"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Activation.ServiceWorkingCopy == "" || file.Activation.SVNBinary == "" {
		t.Fatal("fixture config has no service working copy or svn binary")
	}
	return file.Activation.ServiceWorkingCopy, file.Activation.SVNBinary
}

// writeCanonicalRepositoryFixture commits the record rather than only writing
// it. That is not test decoration: every administrative entry point reconciles
// the service working copy first, and that reconciliation runs
// "svn cleanup --remove-unversioned", which deletes an unversioned record
// before any repair can read it. It is also why hand-editing a canonical record
// in the working copy cannot fix a stalled repository - only a published change
// survives.
func writeCanonicalRepositoryFixture(t *testing.T, serviceWC, svnBinary, repoID string, record map[string]any) string {
	t.Helper()
	dir := filepath.Join(serviceWC, "admin", "repositories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, repoID+".json")
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	runRepoPruneCommand(t, svnBinary, "add", "--force", "--parents", "--non-interactive", path)
	runRepoPruneCommand(t, svnBinary, "commit", "--non-interactive", "-m", "fixture: canonical repository record", serviceWC)
	return path
}

// TestRepoActivateRepairsAStalledInitializingRepository covers the spot
// ARCHIWUM stall of 2026-08-14: CREATE_REPOSITORY succeeded and the initial
// import reached the backend as r1, but the INITIAL_COMMIT control ticket never
// landed, so ServicePublisher.Activate never ran and the canonical record sat
// at "initializing" for 18 days. Clients kept projecting it as not-yet-ready
// and refused to attach, while the mobile client happily appended 92 more
// revisions to the very same repository.
//
// The client resumes that exchange by itself only while its local provisioning
// operation survives; once that file is gone - reinstall, new profile, cleanup
// - nothing on either side can finish the transition. This command is that
// missing repair, and the record it repairs is also brought up to the current
// field shape on the way through.
func TestRepoActivateRepairsAStalledInitializingRepository(t *testing.T) {
	if isolateSandboxingTest(t, "TestRepoActivateRepairsAStalledInitializingRepository") {
		return
	}
	permitRepeatedSandbox(t)
	configPath, _, repositoriesRoot := writeRepoPruneFixtureConfig(t)
	serviceWC, svnBinary := activateFixturePaths(t, configPath)
	realmID := "5b2b2595-312c-4e8f-9407-148e2a174033"
	repoID := "a53c17e1-5f6a-5591-bd0b-17820c4344b2"
	// An older-shaped record: no schema, no url, no created_at.
	recordPath := writeCanonicalRepositoryFixture(t, serviceWC, svnBinary, repoID, map[string]any{
		"repo_id": repoID, "owner_realm_id": realmID,
		"display_name": "ARCHIWUM", "state": "initializing",
	})
	if err := os.MkdirAll(filepath.Join(repositoriesRoot, repoID), 0o700); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := RunAdmin([]string{"-config", configPath, "repo", "activate", "-repo-id", repoID, "-realm-id", realmID}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("repo activate exit=%d stderr=%s", code, stderr.String())
	}
	var result struct {
		Status       string   `json:"status"`
		HealedFields []string `json:"healed_fields"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode activate output: %v: %s", err, stdout.String())
	}
	if result.Status != "activated" {
		t.Fatalf("status = %q, want activated", result.Status)
	}
	if len(result.HealedFields) == 0 {
		t.Fatalf("older-shaped record reported no healed fields: %s", stdout.String())
	}

	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var healed struct {
		Schema string `json:"schema"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(raw, &healed); err != nil {
		t.Fatal(err)
	}
	if healed.State != "active" {
		t.Fatalf("state = %q, want active", healed.State)
	}
	if healed.Schema == "" || healed.URL == "" {
		t.Fatalf("record was activated without being completed first: %s", raw)
	}
	// Idempotency is covered by repoworker.TestCompleteCanonicalRecordNever
	// SerialisesDefaults and by Activate accepting the "active" state, not by a
	// second RunAdmin here: on OpenBSD the first call pledges and unveils the
	// test process for good, so any later administrative call in the same
	// process fails on pledge and every later fixture loses sight of the svn
	// binaries. Each RunAdmin test therefore needs its own process
	// (go test -run one-at-a-time), which is also why these three live in
	// separate top-level tests rather than subtests.
}

// Publishing "ready" for a record with no FSFS repository behind it would hand
// every client a URL that cannot be checked out - a worse failure than the
// stall being repaired, and one the client reports as a transport error with no
// hint that the repository never existed.
func TestRepoActivateRefusesARecordWithNoBackend(t *testing.T) {
	if isolateSandboxingTest(t, "TestRepoActivateRefusesARecordWithNoBackend") {
		return
	}
	permitRepeatedSandbox(t)
	configPath, _, _ := writeRepoPruneFixtureConfig(t)
	serviceWC, svnBinary := activateFixturePaths(t, configPath)
	realmID := "5b2b2595-312c-4e8f-9407-148e2a174033"
	repoID := "a53c17e1-5f6a-5591-bd0b-17820c4344b2"
	recordPath := writeCanonicalRepositoryFixture(t, serviceWC, svnBinary, repoID, map[string]any{
		"schema": "filees.repository/v1", "repo_id": repoID, "owner_realm_id": realmID,
		"display_name": "ARCHIWUM", "state": "initializing",
		"url":        "svn+ssh://_filees-data@filees.test/" + repoID,
		"created_at": "2026-08-14T19:41:19.701822006Z",
	})
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := RunAdmin([]string{"-config", configPath, "repo", "activate", "-repo-id", repoID, "-realm-id", realmID}, &stdout, &stderr); code == ExitOK {
		t.Fatalf("activate accepted a record with no FSFS repository: %s", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("backend is absent")) {
		t.Fatalf("refusal did not explain the missing backend: %s", stderr.String())
	}
	after, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a refused activation still rewrote the record: %s", after)
	}
}

// The ownership check is ServicePublisher.Activate's, not this command's, but
// the command must surface it rather than reporting success: a repair tool that
// activated another realm's repository would be a privilege escalation with an
// audit trail that says "activated".
func TestRepoActivateRefusesAForeignRealm(t *testing.T) {
	if isolateSandboxingTest(t, "TestRepoActivateRefusesAForeignRealm") {
		return
	}
	permitRepeatedSandbox(t)
	configPath, _, repositoriesRoot := writeRepoPruneFixtureConfig(t)
	serviceWC, svnBinary := activateFixturePaths(t, configPath)
	repoID := "a53c17e1-5f6a-5591-bd0b-17820c4344b2"
	writeCanonicalRepositoryFixture(t, serviceWC, svnBinary, repoID, map[string]any{
		"schema": "filees.repository/v1", "repo_id": repoID,
		"owner_realm_id": "5b2b2595-312c-4e8f-9407-148e2a174033",
		"display_name":   "ARCHIWUM", "state": "initializing",
		"url":        "svn+ssh://_filees-data@filees.test/" + repoID,
		"created_at": "2026-08-14T19:41:19.701822006Z",
	})
	if err := os.MkdirAll(filepath.Join(repositoriesRoot, repoID), 0o700); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := RunAdmin([]string{"-config", configPath, "repo", "activate", "-repo-id", repoID, "-realm-id", "8f14e45f-ceea-467e-adde-3fb5787cd831"}, &stdout, &stderr)
	if code == ExitOK {
		t.Fatalf("activate accepted a foreign realm: %s", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("does not own")) {
		t.Fatalf("refusal did not name the ownership failure: %s", stderr.String())
	}
}
