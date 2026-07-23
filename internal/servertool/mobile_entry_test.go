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

func newMobileSeededRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	mobileRun(t, "svnadmin", "create", repo)
	seed := filepath.Join(dir, "seed")
	if err := os.MkdirAll(filepath.Join(seed, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "photos", "a.jpg"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mobileRun(t, "svn", "import", "-q", seed, mobileFileURL(repo), "-m", "seed import")
	return repo
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
// as the filees-mobile-v1 forced command would: a real seeded SVN repo, a real
// mobileworker.Dispatcher, one framed REFRESH_MANIFEST request in and one
// framed response out. On OpenBSD this re-execs the test binary in a child
// process so the real pledge/unveil syscalls in obsandbox.Apply cannot
// permanently narrow the shared `go test` process (mirrors
// TestClientEntrySeparatesProofFromForcedSVNCommand's isolation).
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

	repo := newMobileSeededRepo(t)
	root := t.TempDir()
	ledgerDir := filepath.Join(root, "ledger")
	configPath := filepath.Join(root, "mobile-repos.json")

	grants := []MobileRepoGrant{{ClientID: "device-1", RepoID: "repo-1", RepoPath: repo, Access: "rw", Generation: 9}}
	raw, err := json.Marshal(grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	req, err := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo-1"})
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
	code := runMobileEntry(configPath, ledgerDir, []string{"op-1", "device-1"}, mobileOperationalGetenv, &stdin, &stdout, &stderr, mobileNeverExec(t))
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

func TestMobileEntryRejectsUngrantedClient(t *testing.T) {
	repo := newMobileSeededRepo(t)
	root := t.TempDir()
	configPath := filepath.Join(root, "mobile-repos.json")
	grants := []MobileRepoGrant{{ClientID: "device-1", RepoID: "repo-1", RepoPath: repo, Access: "rw", Generation: 1}}
	raw, _ := json.Marshal(grants)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	req, err := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo-1"})
	if err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(req)
	var stdin bytes.Buffer
	if err := v1.WriteFrame(&stdin, v1.RequestMagic, header, nil); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runMobileEntry(configPath, filepath.Join(root, "ledger"), []string{"op-1", "someone-else"}, mobileOperationalGetenv, &stdin, &stdout, &stderr, mobileNeverExec(t))
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
	if resp.Status != v1.StatusError {
		t.Fatalf("expected an error response for an ungranted client, got %v", resp.Status)
	}
}
