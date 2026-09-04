package servertool

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/clientview"
	v1 "filees/pkg/mobile/v1"

	"github.com/google/uuid"
)

func mobileRequireSVN(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func mobileRun(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, errb.String())
	}
}

func mobileFileURL(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// newMobileSeededRepoAt creates a real SVN repository at the exact path a
// clientviewMobileAuthority-resolved grant would point to
// (RepositoriesRoot/repoID), seeded with one file so REFRESH_MANIFEST has
// something to report.
func newMobileSeededRepoAt(t *testing.T, repoPath string) {
	t.Helper()
	mobileRun(t, "svnadmin", "create", repoPath)
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(filepath.Join(seed, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "photos", "a.jpg"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mobileRun(t, "svn", "import", "-q", seed, mobileFileURL(repoPath), "-m", "seed import")
}

// writeMobileClientView publishes clients/<clientID>/view.json under the
// given service working copy, exactly the projection
// pkg/activation.Manager.Publish/pkg/repoworker.ServicePublisher maintain in
// production - this is what clientviewMobileAuthority.Resolve reads.
func writeMobileClientView(t *testing.T, serviceWC, clientID, realmID string, generation int64, repos []clientview.Repository) {
	t.Helper()
	view := clientview.View{
		Schema:            clientview.Schema,
		ServerDisplayName: "Serwer testowy",
		ClientID:          clientID,
		RealmID:           realmID,
		Generation:        generation,
		GeneratedAt:       time.Now().UTC(),
		ClientRole:        "normal",
		Repositories:      repos,
	}
	viewPath := filepath.Join(serviceWC, "clients", clientID, "view.json")
	if _, err := clientview.StoreIfNewer(viewPath, view); err != nil {
		t.Fatal(err)
	}
}

func mobileGrantedRepository(repoID, access string) clientview.Repository {
	return clientview.Repository{
		RepoID:      repoID,
		DisplayName: "repo-1",
		URL:         "svn+ssh://" + url.User("_filees-client").String() + "@filees.test/" + repoID,
		Access:      access,
		State:       "active",
	}
}

func mobileOperationalGetenv(key string) string {
	if key == "SSH_ORIGINAL_COMMAND" {
		return MobileOperationalCommand
	}
	return ""
}

func mobileNeverExec(t *testing.T) func(string, string, string) error {
	return func(subcommand, operationID, clientID string) error {
		t.Fatalf("unexpected exec: %s %s %s", subcommand, operationID, clientID)
		return nil
	}
}

func TestMobileEntryRejectsBadArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exec := mobileNeverExec(t)
	if code := runMobileEntry("/nonexistent", t.TempDir(), nil, mobileOperationalGetenv, &bytes.Buffer{}, &stdout, &stderr, exec); code != ExitUnavailable {
		t.Fatalf("no args: code=%d", code)
	}
	if code := runMobileEntry("/nonexistent", t.TempDir(), []string{"op-1"}, mobileOperationalGetenv, &bytes.Buffer{}, &stdout, &stderr, exec); code != ExitUnavailable {
		t.Fatalf("one arg: code=%d", code)
	}
	if code := runMobileEntry("/nonexistent", t.TempDir(), []string{"", "device-1"}, mobileOperationalGetenv, &bytes.Buffer{}, &stdout, &stderr, exec); code != ExitUnavailable {
		t.Fatalf("empty operation id: code=%d", code)
	}
	if code := runMobileEntry("/nonexistent", t.TempDir(), []string{"op-1", ""}, mobileOperationalGetenv, &bytes.Buffer{}, &stdout, &stderr, exec); code != ExitUnavailable {
		t.Fatalf("empty client id: code=%d", code)
	}
	if code := runMobileEntry("/nonexistent", t.TempDir(), []string{"op-1", "device-1", "extra"}, mobileOperationalGetenv, &bytes.Buffer{}, &stdout, &stderr, exec); code != ExitUnavailable {
		t.Fatalf("too many args: code=%d", code)
	}
}

func TestMobileEntryRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		if key == "SSH_ORIGINAL_COMMAND" {
			return "something else"
		}
		return ""
	}
	code := runMobileEntry("/nonexistent", t.TempDir(), []string{"op-1", "device-1"}, getenv, &bytes.Buffer{}, &stdout, &stderr, mobileNeverExec(t))
	if code != ExitUnavailable {
		t.Fatalf("code=%d", code)
	}
}

func TestMobileEntryDispatchesProofAndFinishToExec(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, tc := range []struct {
		command, wantSubcommand string
	}{
		{MobileProofCommand, "mobile-proof"},
		{MobileFinishCommand, "mobile-finish"},
	} {
		var gotSubcommand, gotOp, gotClient string
		exec := func(subcommand, operationID, clientID string) error {
			gotSubcommand, gotOp, gotClient = subcommand, operationID, clientID
			return nil
		}
		getenv := func(key string) string {
			if key == "SSH_ORIGINAL_COMMAND" {
				return tc.command
			}
			return ""
		}
		code := runMobileEntry("/nonexistent", t.TempDir(), []string{"op-1", "device-1"}, getenv, &bytes.Buffer{}, &stdout, &stderr, exec)
		if code != ExitOK || gotSubcommand != tc.wantSubcommand || gotOp != "op-1" || gotClient != "device-1" {
			t.Fatalf("command=%s: code=%d subcommand=%s op=%s client=%s", tc.command, code, gotSubcommand, gotOp, gotClient)
		}
	}
}

func TestMobileEntryReportsExecFailureForProofAndFinish(t *testing.T) {
	var stdout, stderr bytes.Buffer
	failing := func(string, string, string) error { return errors.New("exec failed") }
	getenv := func(key string) string {
		if key == "SSH_ORIGINAL_COMMAND" {
			return MobileProofCommand
		}
		return ""
	}
	code := runMobileEntry("/nonexistent", t.TempDir(), []string{"op-1", "device-1"}, getenv, &bytes.Buffer{}, &stdout, &stderr, failing)
	if code != ExitSoftware {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestMobileEntryRejectsMissingConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMobileEntry(filepath.Join(t.TempDir(), "missing.json"), t.TempDir(), []string{"op-1", "device-1"}, mobileOperationalGetenv, &bytes.Buffer{}, &stdout, &stderr, mobileNeverExec(t))
	if code != ExitConfig {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestMobileEntryServesRefreshOverRealDispatcher drives runMobileEntry exactly
// as the filees-mobile-v1 forced command would: a real server.json, a real
// published clients/<clientID>/view.json granting one repository (the same
// projection pkg/activation.Manager.Publish/pkg/repoworker.ServicePublisher
// maintain in production, not a static stub), a real seeded SVN repo at the
// resolved repository-root path, a real mobileworker.Dispatcher, one framed
// REFRESH_MANIFEST request in and one framed response out. On OpenBSD this
// re-execs the test binary in a child process so the real pledge/unveil
// syscalls in obsandbox.Apply cannot permanently narrow the shared `go test`
// process (mirrors TestClientEntrySeparatesProofFromForcedSVNCommand's
// isolation).
func TestMobileEntryServesRefreshOverRealDispatcher(t *testing.T) {
	mobileRequireSVN(t)

	if runtime.GOOS == "openbsd" && os.Getenv("FILEES_MOBILE_ENTRY_NATIVE") == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestMobileEntryServesRefreshOverRealDispatcher$", "-test.v")
		command.Env = append(os.Environ(), "FILEES_MOBILE_ENTRY_NATIVE=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("native mobile entry child: %v: %s", err, output)
		}
		return
	}

	f := newMobileWorkerFixture(t)
	clientID, repoID := uuid.NewString(), uuid.NewString()
	newMobileSeededRepoAt(t, filepath.Join(f.repositoriesRoot, repoID))
	writeMobileClientView(t, f.serviceWC, clientID, f.realmID, 9, []clientview.Repository{mobileGrantedRepository(repoID, "rw")})

	req, err := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: repoID})
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var stdin bytes.Buffer
	if err := v1.WriteFrame(&stdin, v1.RequestMagic, header, nil); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runMobileEntry(f.configPath, filepath.Join(t.TempDir(), "ledger"), []string{"op-1", clientID}, mobileOperationalGetenv, &stdin, &stdout, &stderr, mobileNeverExec(t))
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}

	respHeader, _, err := v1.ReadFrame(&stdout, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	resp, err := v1.ParseResponse(respHeader)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %v, error = %+v", resp.Status, resp.Error)
	}
	var result v1.RefreshManifestResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Manifest == nil || result.Manifest.ViewGeneration != 9 || result.Manifest.RepoRevision != 1 {
		t.Fatalf("manifest = %+v", result.Manifest)
	}
}

func TestMobileEntryServesListRepositories(t *testing.T) {
	if isolateSandboxingTest(t, "TestMobileEntryServesListRepositories") {
		return
	}
	permitRepeatedSandbox(t)
	f := newMobileWorkerFixture(t)
	clientID, repoID := uuid.NewString(), uuid.NewString()
	writeMobileClientView(t, f.serviceWC, clientID, f.realmID, 3, []clientview.Repository{mobileGrantedRepository(repoID, "rw")})

	req, err := v1.NewRequest(uuid.NewString(), v1.OpListRepositories, v1.ListRepositoriesPayload{})
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var stdin bytes.Buffer
	if err := v1.WriteFrame(&stdin, v1.RequestMagic, header, nil); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runMobileEntry(f.configPath, filepath.Join(t.TempDir(), "ledger"), []string{"op-1", clientID}, mobileOperationalGetenv, &stdin, &stdout, &stderr, mobileNeverExec(t))
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	respHeader, _, err := v1.ReadFrame(&stdout, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	resp, err := v1.ParseResponse(respHeader)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %v, error = %+v", resp.Status, resp.Error)
	}
	var result v1.ListRepositoriesResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ViewGeneration != 3 || result.RealmID != f.realmID || len(result.Repositories) != 1 {
		t.Fatalf("projection = %+v", result)
	}
	got := result.Repositories[0]
	if got.RepoID != repoID || got.DisplayName != "repo-1" || got.Access != "rw" || got.State != "active" {
		t.Fatalf("share = %+v", got)
	}
}

// TestMobileEntryForwardsRepositoryPurpose guards the whole
// clientview.Repository.Purpose -> RepositoryGrant -> RepositorySummary
// wire chain: an upload shelf must reach the client tagged as such, so it
// can be grouped apart from ordinary repositories instead of listed mixed
// in - the desktop projection already makes the same distinction (r613).
func TestMobileEntryForwardsRepositoryPurpose(t *testing.T) {
	if isolateSandboxingTest(t, "TestMobileEntryForwardsRepositoryPurpose") {
		return
	}
	permitRepeatedSandbox(t)
	f := newMobileWorkerFixture(t)
	clientID, repoID := uuid.NewString(), uuid.NewString()
	shelf := mobileGrantedRepository(repoID, "rw")
	shelf.Purpose = clientview.PurposeUploadShelf
	writeMobileClientView(t, f.serviceWC, clientID, f.realmID, 3, []clientview.Repository{shelf})

	req, err := v1.NewRequest(uuid.NewString(), v1.OpListRepositories, v1.ListRepositoriesPayload{})
	if err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var stdin bytes.Buffer
	if err := v1.WriteFrame(&stdin, v1.RequestMagic, header, nil); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runMobileEntry(f.configPath, filepath.Join(t.TempDir(), "ledger"), []string{"op-1", clientID}, mobileOperationalGetenv, &stdin, &stdout, &stderr, mobileNeverExec(t))
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	respHeader, _, err := v1.ReadFrame(&stdout, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	resp, err := v1.ParseResponse(respHeader)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %v, error = %+v", resp.Status, resp.Error)
	}
	var result v1.ListRepositoriesResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Purpose != clientview.PurposeUploadShelf {
		t.Fatalf("projection = %+v", result)
	}
}

func TestMobileEntryRejectsUngrantedClient(t *testing.T) {
	if isolateSandboxingTest(t, "TestMobileEntryRejectsUngrantedClient") {
		return
	}
	permitRepeatedSandbox(t)
	f := newMobileWorkerFixture(t)
	clientID, repoID := uuid.NewString(), uuid.NewString()
	newMobileSeededRepoAt(t, filepath.Join(f.repositoriesRoot, repoID))
	writeMobileClientView(t, f.serviceWC, clientID, f.realmID, 1, []clientview.Repository{mobileGrantedRepository(repoID, "rw")})

	req, err := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: repoID})
	if err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(req)
	var stdin bytes.Buffer
	if err := v1.WriteFrame(&stdin, v1.RequestMagic, header, nil); err != nil {
		t.Fatal(err)
	}

	// someone-else has no published view.json at all - this is the mobile
	// equivalent of a client that was never granted anything.
	someoneElse := uuid.NewString()
	var stdout, stderr bytes.Buffer
	code := runMobileEntry(f.configPath, filepath.Join(t.TempDir(), "ledger"), []string{"op-1", someoneElse}, mobileOperationalGetenv, &stdin, &stdout, &stderr, mobileNeverExec(t))
	if code != ExitOK {
		// The dispatcher itself turns an authority error into a StatusError
		// response, not a process failure -- runMobileEntry only fails hard on
		// a transport-level problem.
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	respHeader, _, err := v1.ReadFrame(&stdout, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	resp, err := v1.ParseResponse(respHeader)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != v1.StatusError || resp.Error == nil || resp.Error.Code != "access.denied" {
		t.Fatalf("expected access.denied for an ungranted client, got %+v", resp)
	}
}
