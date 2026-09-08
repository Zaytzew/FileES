package servertool

import (
	"bytes"
	"encoding/json"
	"filees/internal/svnurl"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"filees/pkg/passport"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

func TestPassportMaintenanceProcess(t *testing.T) {
	if config := os.Getenv("FILEES_MAINTENANCE_CONFIG"); config != "" {
		args := []string{"-config", config}
		if os.Getenv("FILEES_MAINTENANCE_CHECK") == "1" {
			args = append(args, "--check")
		}
		os.Exit(runPassportReap(args, os.Stdout, os.Stderr, os.Args[0]))
	}
}

func TestPassportMaintenanceProductionSandboxRealSVN(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix server")
	}
	path, results, repos := writeRepoPruneFixtureConfig(t)
	config, err := serverconfig.LoadFor(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, realm, actor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo, wc := filepath.Join(repos, id), filepath.Join(t.TempDir(), "wc")
	svn, admin := config.Activation.SVNBinary, config.Repositories.SVNAdminBinary
	runRepoPruneCommand(t, admin, "create", repo)
	runRepoPruneCommand(t, svn, "co", svnurl.File(repo), wc)
	doc := filepath.Join(wc, "file")
	if err := os.WriteFile(doc, []byte("keep these bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	runRepoPruneCommand(t, svn, "add", doc)
	runRepoPruneCommand(t, svn, "ci", "-m", "fixture", wc)
	service := config.Activation.ServiceWorkingCopy
	for rel, value := range map[string]any{
		"admin/clients/" + actor + ".json":   map[string]string{"schema": "filees.client-instance/v1", "client_id": actor, "realm_id": realm, "state": "active"},
		"admin/realms/" + realm + ".json":    map[string]string{"schema": "filees.realm/v1", "realm_id": realm, "state": "active"},
		"admin/repositories/" + id + ".json": map[string]string{"schema": repoworker.RepositorySchema, "repo_id": id, "owner_realm_id": realm, "state": "active"},
	} {
		writeRepoPruneJSON(t, filepath.Join(service, rel), value)
	}
	if err := repoworker.InstallLockGuards(repo, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	meta := passport.Metadata{AcquisitionID: uuid.NewString(), PassportID: uuid.NewString(), InstanceUID: uuid.NewString(), IssuedAt: now, ExpiresAt: now.Add(time.Minute), HardExpiresAt: now.Add(time.Hour)}
	svc := repoworker.PassportExecutions{Authority: repoworker.PassportReplacementAuthority{ServiceWC: service, Locks: repoworker.SVNAdminLockAuthority{SVNAdmin: admin, RepositoriesRoot: repos}}, GuardExecutable: os.Args[0]}
	session := repoworker.Session{ClientID: actor, RealmID: realm, Repositories: []clientview.Repository{{RepoID: id, OwnerRealmID: realm, Access: "rw", State: "active"}}}
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketArmPassportAcquisition, actor, control.PassportExecutionPayload{RepoID: id, Path: "file", Comment: passport.FormatComment(meta)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := svc.Handle(t.Context(), session, ticket); err != nil || r.Status != control.ResultOK {
		t.Fatalf("arm: %+v %v", r, err)
	}
	runRepoPruneCommand(t, svn, "lock", "--username", actor, "-m", passport.FormatComment(meta), doc)
	// Deterministic expiry fixture, without moving the host clock or waiting
	// a minute: replace this private test lock/journal's time metadata together.
	meta.IssuedAt = now.Add(-2 * time.Minute)
	meta.ExpiresAt = now.Add(-time.Minute)
	journal := filepath.Join(repos, ".filees-passport-executions", id, meta.AcquisitionID+".json")
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	// Release only the fixture's known token, then re-create the historical
	// expired token with svnadmin and bypass admission only in fixture setup.
	info, err := svc.Authority.Locks.InspectLock(t.Context(), id, "file")
	if err != nil || info == nil {
		t.Fatal(err)
	}
	runRepoPruneCommand(t, admin, "unlock", "--", repo, "/file", actor, info.ObservedLockID)
	commentFile := filepath.Join(t.TempDir(), "comment")
	if err := os.WriteFile(commentFile, []byte(passport.FormatComment(meta)), 0600); err != nil {
		t.Fatal(err)
	}
	runRepoPruneCommand(t, admin, "lock", "--bypass-hooks", repo, "/file", actor, commentFile, info.ObservedLockID)
	record["comment"] = passport.FormatComment(meta)
	record["expires_at"] = meta.ExpiresAt
	writeRepoPruneJSON(t, journal, record)
	// The scheduled mode must not need authority records or activation files.
	if err := os.Rename(service, service+"-offline"); err != nil {
		t.Fatal(err)
	}
	run := func(check bool, want int) {
		t.Helper()
		child := exec.Command(os.Args[0], "-test.run=^TestPassportMaintenanceProcess$")
		child.Env = append(os.Environ(), "FILEES_MAINTENANCE_CONFIG="+path)
		if check {
			child.Env = append(child.Env, "FILEES_MAINTENANCE_CHECK=1")
		}
		out, err := child.CombinedOutput()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("maintenance check=%v exit=%d want=%d: %s", check, code, want, out)
		}
	}
	run(false, ExitOK)
	run(true, ExitOK)
	if lock, err := svc.Authority.Locks.InspectLock(t.Context(), id, "file"); err != nil || lock != nil {
		t.Fatalf("expired lock: %+v %v", lock, err)
	}
	runRepoPruneCommand(t, svn, "lock", "--username", actor, "-m", "new ordinary token", doc)
	next, err := svc.Authority.Locks.InspectLock(t.Context(), id, "file")
	if err != nil || next == nil {
		t.Fatal(err)
	}
	run(false, ExitOK)
	after, err := svc.Authority.Locks.InspectLock(t.Context(), id, "file")
	if err != nil || after == nil || after.ObservedLockID != next.ObservedLockID {
		t.Fatal("successor changed")
	}
	statusFile := filepath.Join(results, "passport-maintenance", "status.json")
	statusRaw, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusFile, append(statusRaw, []byte("trailing corruption")...), 0600); err != nil {
		t.Fatal(err)
	}
	run(true, ExitTempFail)
	run(false, ExitOK)
	writeRepoPruneJSON(t, statusFile, repoworker.PassportReapStatus{Schema: "filees.passport-maintenance/v1", State: "running", StartedAt: now})
	run(true, ExitTempFail)
	run(false, ExitOK)
	run(true, ExitOK)
	if data, _ := os.ReadFile(doc); string(data) != "keep these bytes" {
		t.Fatal("changed bytes")
	}
}

func TestPassportMaintenanceLeastPrivilegeAndUsage(t *testing.T) {
	config := serverconfig.Config{}
	config.Repositories.Root = "/srv/repos"
	config.Repositories.ResultsRoot = "/srv/results"
	config.Repositories.SVNAdminBinary = "/usr/local/bin/svnadmin"
	config.Activation.Root = "/srv/activation"
	profile := passportMaintenanceProfile(config, "/srv/results/passport-maintenance", false, "/usr/local/libexec/filees/filees-worker")
	if strings.Contains(profile.Promises, "inet") || strings.Contains(profile.Promises, "dns") {
		t.Fatal("network promises")
	}
	for _, path := range profile.Paths {
		if path.Name == config.Activation.Root || path.Name == config.Repositories.ResultsRoot || strings.Contains(path.Label, "authz") {
			t.Fatalf("excessive authority: %+v", path)
		}
	}
	check := passportMaintenanceProfile(config, "/srv/results/passport-maintenance", true, "")
	if len(check.Paths) != 1 || check.Promises != "stdio rpath" || check.Paths[0].Perms != "r" {
		t.Fatal("health check can mutate")
	}
	var out, stderr bytes.Buffer
	if RunPassportReap([]string{"--force"}, &out, &stderr) != ExitUsage {
		t.Fatal("unexpected option accepted")
	}
}
