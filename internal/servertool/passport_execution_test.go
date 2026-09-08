//go:build !windows

// The runtime t.Skip("server is Unix") inside is not enough on its own: this
// file uses runSupervisorCommand from session_supervisor_unix_test.go, which
// is //go:build !windows, so without the same tag the whole package fails to
// COMPILE on Windows - and a package that does not build does not skip, it
// disappears. Every test in internal/servertool was therefore absent from
// Windows runs, counted as one more line in the "~190 environmental" bucket.

package servertool

import (
	"bytes"
	"encoding/json"
	"filees/internal/svnurl"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"filees/pkg/passport"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

func TestPassportProductionHookRealSVN(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("server is Unix")
	}
	tools := requireSVN(t, "svn", "svnadmin")
	svn, admin := tools[0], tools[1]
	root := t.TempDir()
	repoID, realmID, clientID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo, wc, service := filepath.Join(root, repoID), filepath.Join(root, "wc"), filepath.Join(root, "service")
	runSupervisorCommand(t, admin, "create", repo)
	runSupervisorCommand(t, svn, "co", svnurl.File(repo), wc)
	doc := filepath.Join(wc, "doc")
	if err := os.WriteFile(doc, []byte("unchanged bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	runSupervisorCommand(t, svn, "add", doc)
	runSupervisorCommand(t, svn, "ci", "-m", "fixture", wc)
	put := func(rel string, value any) {
		t.Helper()
		file := filepath.Join(service, rel)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("admin/clients/"+clientID+".json", map[string]string{"schema": "filees.client-instance/v1", "client_id": clientID, "realm_id": realmID, "state": "active"})
	put("admin/realms/"+realmID+".json", map[string]string{"schema": "filees.realm/v1", "realm_id": realmID, "state": "active"})
	put("admin/repositories/"+repoID+".json", map[string]string{"schema": repoworker.RepositorySchema, "repo_id": repoID, "owner_realm_id": realmID, "state": "active"})
	if err := repoworker.InstallLockGuards(repo, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	svc := repoworker.PassportExecutions{Authority: repoworker.PassportReplacementAuthority{ServiceWC: service, Locks: repoworker.SVNAdminLockAuthority{SVNAdmin: admin, RepositoriesRoot: root}}, GuardExecutable: os.Args[0]}
	session := repoworker.Session{ClientID: clientID, RealmID: realmID, Repositories: []clientview.Repository{{RepoID: repoID, OwnerRealmID: realmID, State: "active", Access: "rw"}}}
	now := time.Now().UTC()
	meta := passport.Metadata{AcquisitionID: uuid.NewString(), PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), RealmID: realmID, IssuedAt: now, ExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(time.Hour)}
	comment := passport.FormatComment(meta)
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketArmPassportAcquisition, clientID, control.PassportExecutionPayload{RepoID: repoID, Path: "doc", Comment: comment}, now)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := svc.Handle(t.Context(), session, ticket); err != nil || r.Status != control.ResultOK {
		t.Fatalf("arm: %+v %v", r, err)
	}
	args := []string{svn, "lock", "--username", clientID, "-m", comment, doc}
	command := exec.Command(svn, args[1:]...)
	command.Dir = root
	if runtime.GOOS == "openbsd" {
		// Reuse the native pledge + locked-unveil launcher, executing the same
		// production RunLockGuardMode as the installed worker image.
		raw, _ := json.Marshal(args)
		command = exec.Command(os.Args[0], "-test.run=^TestLockGuardsUnderSVNChildPromises$")
		command.Dir = root
		command.Env = append(os.Environ(), "FILEES_TEST_HOOK_ARGV="+string(raw), "FILEES_TEST_HOOK_UNVEIL="+root)
	}
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("production hook: %v %s", err, out)
	}
	file := filepath.Join(root, ".filees-passport-executions", repoID, meta.AcquisitionID+".json")
	raw, err := os.ReadFile(file)
	var record struct {
		State       string
		ExecutorPID int `json:"executor_pid"`
	}
	if err != nil || json.Unmarshal(raw, &record) != nil || record.State != "started" || record.ExecutorPID <= 1 {
		t.Fatalf("admission not durable: %s %v", raw, err)
	}
	ticket.Type = control.TicketSettlePassportAcquisition
	if r, err := svc.Handle(t.Context(), session, ticket); err != nil || r.Status != control.ResultOK {
		t.Fatalf("settle: %+v %v", r, err)
	}
	if out, err := exec.Command(svn, args[1:]...).CombinedOutput(); err == nil {
		t.Fatalf("replay admitted: %s", out)
	}
	if data, _ := os.ReadFile(doc); string(data) != "unchanged bytes" {
		t.Fatal("bytes changed")
	}
}

func TestPassportHookLeastPromises(t *testing.T) {
	original := sandboxNarrow
	t.Cleanup(func() { sandboxNarrow = original })
	meta := passport.Metadata{AcquisitionID: uuid.NewString(), PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute), HardExpiresAt: time.Now().Add(time.Hour)}
	for _, tc := range []struct {
		program string
		args    []string
		want    string
	}{
		{"pre-lock", []string{"repo", "/doc", "client", "ordinary", "0"}, "stdio"},
		{"pre-unlock", []string{"repo", "/doc", "client", "token", "0"}, "stdio"},
		{"pre-lock", []string{"repo", "/doc", "client", passport.FormatComment(meta), "1"}, "stdio"},
		{"pre-lock", []string{"repo", "/doc", "client", passport.FormatComment(meta), "0"}, "stdio rpath wpath cpath fattr flock"},
	} {
		var got string
		sandboxNarrow = func(p string) error { got = p; return nil }
		var out, stderr bytes.Buffer
		handled, _ := RunLockGuardMode(tc.program, tc.args, &out, &stderr)
		if !handled || got != tc.want || out.Len() != 0 {
			t.Fatalf("sandbox/stdout: %q %q %q", tc.program, got, out.String())
		}
	}
}

func TestPassportReapRequiresScope(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := RunAdmin([]string{"repo", "reap-passports"}, &out, &stderr); code != ExitUsage {
		t.Fatalf("unscoped reap: %d %s", code, stderr.String())
	}
}
