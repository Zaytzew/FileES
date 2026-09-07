package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

func scheduledFixture(t *testing.T, f *executionFixture) ScheduledPassportReaper {
	t.Helper()
	dir := t.TempDir()
	return ScheduledPassportReaper{Executions: f.svc, WorkerLock: filepath.Join(dir, "worker.lock"), ServiceLock: filepath.Join(dir, "service.lock"), StatusFile: filepath.Join(dir, "status.json")}
}

func TestScheduledPassportReapChild(t *testing.T) {
	if os.Getenv("FILEES_REAP_CRASH") != "1" {
		return
	}
	var reaper ScheduledPassportReaper
	if err := json.Unmarshal([]byte(os.Getenv("FILEES_REAP_PATHS")), &reaper); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, os.Getenv("FILEES_REAP_TIME"))
	if err != nil {
		t.Fatal(err)
	}
	reaper.Executions.Authority.Now = func() time.Time { return expires }
	reaper.Executions.Authority.Locks.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := runLockAuthorityCommand(ctx, name, args...)
		if args[0] == "unlock" && err == nil {
			os.Exit(86)
		}
		return out, err
	}
	_, err = reaper.Run(t.Context())
	t.Fatalf("expected process death after successful unlock: %v", err)
}

func TestScheduledPassportReapCrashAndRestartWithoutClient(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	reaper := scheduledFixture(t, f)
	// Serialize only fixture paths: authority contains injectable functions.
	paths := map[string]any{"WorkerLock": reaper.WorkerLock, "ServiceLock": reaper.ServiceLock, "StatusFile": reaper.StatusFile,
		"Executions": map[string]any{"Authority": map[string]any{"Locks": map[string]string{"SVNAdmin": f.svc.Authority.Locks.SVNAdmin, "RepositoriesRoot": f.svc.Authority.Locks.RepositoriesRoot}}}}
	raw, _ := json.Marshal(paths)
	child := exec.Command(os.Args[0], "-test.run=^TestScheduledPassportReapChild$")
	child.Env = append(os.Environ(), "FILEES_REAP_CRASH=1", "FILEES_REAP_PATHS="+string(raw), "FILEES_REAP_TIME="+f.metadata.ExpiresAt.Format(time.RFC3339Nano))
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("child: %v %s", err, out)
	}
	var status PassportReapStatus
	raw, err = os.ReadFile(reaper.StatusFile)
	if err != nil || json.Unmarshal(raw, &status) != nil || status.State != "running" {
		t.Fatalf("crash status: %s %v", raw, err)
	}
	if f.record(t).State != "closing" || f.record(t).ObservedToken == "" {
		t.Fatal("missing durable token fence")
	}
	replacementCommand(t, "svn", "lock", "--username", f.session.ClientID, "-m", "successor after process death", f.doc)
	before, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || before == nil {
		t.Fatal(err)
	}
	// No active client/realm/grant or service-WC is consulted by maintenance.
	reaper.Executions.Authority.ServiceWC = filepath.Join(t.TempDir(), "absent-service-wc")
	reaper.Executions.Authority.Now = func() time.Time { return f.metadata.ExpiresAt }
	status, err = reaper.Run(t.Context())
	if err != nil || CheckPassportMaintenance(status, f.metadata.ExpiresAt, time.Minute) != nil {
		t.Fatalf("restart: %+v %v", status, err)
	}
	after, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || after == nil || after.Token != before.Token || f.record(t).State != "closed" {
		t.Fatalf("successor/fence: %+v %v", after, err)
	}
	if data, _ := os.ReadFile(f.doc); string(data) != "local work" {
		t.Fatal("changed bytes")
	}
}

func TestScheduledPassportReapBusyAndCorruptIsolation(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	reaper := scheduledFixture(t, f)
	status, err := reaper.Run(t.Context())
	if err != nil || status.State != "checked" || f.record(t).State != "started" {
		t.Fatalf("early reap: %+v %v", status, err)
	}
	for _, lock := range []string{reaper.WorkerLock, reaper.ServiceLock} {
		if err := WithFileLock(lock, func() error {
			_, err := reaper.Run(t.Context())
			if !errors.Is(err, ErrFileLockBusy) {
				t.Fatalf("busy scheduler queued: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	busy := uuid.NewString()
	corrupt := filepath.Join(executionDirectory(f.repo), uuid.NewString()+".json")
	if err := os.WriteFile(corrupt, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(executionDirectory(f.repo), busy+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	reaper.Executions.Authority.Now = func() time.Time { return f.metadata.ExpiresAt }
	if err := WithFileLock(filepath.Join(executionDirectory(f.repo), busy+".lock"), func() error {
		status, err = reaper.Run(t.Context())
		if !errors.Is(err, ErrFileLockBusy) || status.State != "failed" || f.record(t).State != "closed" {
			t.Fatalf("isolated progress: %+v %v", status, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path); err != nil || lock != nil {
		t.Fatalf("expired lock survived: %+v %v", lock, err)
	}
}

func TestScheduledPassportReapCancelledAndHealth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("server Unix locks")
	}
	root := t.TempDir()
	s := ScheduledPassportReaper{Executions: PassportExecutions{Authority: PassportReplacementAuthority{Locks: SVNAdminLockAuthority{RepositoriesRoot: root}}}, WorkerLock: filepath.Join(root, "worker.lock"), ServiceLock: filepath.Join(root, "service.lock"), StatusFile: filepath.Join(root, "status.json")}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	good := PassportReapStatus{Schema: "filees.passport-maintenance/v1", State: "checked", StartedAt: now.Add(-time.Second), FinishedAt: now}
	if err := CheckPassportMaintenance(good, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, clock := range []time.Time{now.Add(2 * time.Minute), now.Add(-time.Second)} {
		if CheckPassportMaintenance(good, clock, time.Minute) == nil {
			t.Fatal("stale/rolled back health accepted")
		}
	}
	for _, state := range []string{"running", "failed", "busy", ""} {
		bad := good
		bad.State = state
		if CheckPassportMaintenance(bad, now, time.Minute) == nil {
			t.Fatal("false healthy")
		}
	}
}

func TestScheduledPassportReapCommandTimeoutAndRetry(t *testing.T) {
	f := newExecutionFixture(t)
	f.handle(t, control.TicketArmPassportAcquisition, control.ResultOK)
	f.lock(t)
	s := scheduledFixture(t, f)
	s.Executions.Authority.Now = func() time.Time { return f.metadata.ExpiresAt }
	s.Executions.Authority.Locks.Run = func(ctx context.Context, _ string, _ ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	status, err := s.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || status.State != "failed" || f.record(t).State != "closing" {
		t.Fatalf("deadline: %+v %v", status, err)
	}
	s.Executions.Authority.Locks.Run = nil
	status, err = s.Run(t.Context())
	if err != nil || status.State != "checked" || f.record(t).State != "closed" {
		t.Fatalf("retry: %+v %v", status, err)
	}
}

func TestScheduledPassportReapRejectsUnavailableOrUnsafeState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix server")
	}
	root := t.TempDir()
	if ValidateMaintenanceRoot(filepath.Join(root, "missing")) == nil || ValidateMaintenanceRoot("/") == nil {
		t.Fatal("unavailable/broad root accepted")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if ValidateMaintenanceRoot(alias) == nil {
		t.Fatal("symlink root accepted")
	}
	svc := PassportExecutions{Authority: PassportReplacementAuthority{Locks: SVNAdminLockAuthority{RepositoriesRoot: root}}}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".filees-passport-executions")); err != nil {
		t.Fatal(err)
	}
	if svc.Reap(t.Context()) == nil {
		t.Fatal("symlink registry reported healthy")
	}
}
