//go:build !windows

package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

type archiveReleaseProcessFixture struct {
	Freeze     archiveFreezeProcessFixture
	Request    ArchiveReleaseRequest
	Checkpoint string
}

func (f archiveReleaseProcessFixture) service() ArchiveReleaseService {
	return ArchiveReleaseService{ArchiveFreezeService: f.Freeze.service(), Administrator: func(context.Context, string, RepositoryArchivePolicy) (string, error) {
		return "test-administrator", nil
	}}
}

func archiveReleaseNativeFixture(t *testing.T) (archiveReleaseProcessFixture, archiveGuardNative) {
	t.Helper()
	f, native := archiveFreezeNativeFixture(t)
	policy, err := f.freeze(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	req := ArchiveReleaseRequest{RepoID: f.Plan.RepoID, OperationID: uuid.NewString(), ExpectedOperationID: policy.OperationID, ExpectedEpoch: policy.Epoch, RepositoryUUID: policy.RepositoryUUID, Revision: policy.Revision, Reason: "isolated administrative release"}
	return archiveReleaseProcessFixture{Freeze: f, Request: req}, native
}

func TestArchiveReleaseNativeAuthorityAndBinding(t *testing.T) {
	f, native := archiveReleaseNativeFixture(t)
	s := f.service()
	for _, kind := range []string{"no authority", "denied", "actor", "actor changed", "epoch", "operation", "uuid", "revision", "reason", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			service, req, ctx := s, f.Request, t.Context()
			switch kind {
			case "no authority":
				service.Administrator = nil
			case "denied":
				service.Administrator = func(context.Context, string, RepositoryArchivePolicy) (string, error) {
					return "", errors.New("not administrator")
				}
			case "actor":
				service.Administrator = func(context.Context, string, RepositoryArchivePolicy) (string, error) { return "", nil }
			case "epoch":
				req.ExpectedEpoch++
			case "operation":
				req.ExpectedOperationID = uuid.NewString()
			case "uuid":
				req.RepositoryUUID = uuid.NewString()
			case "revision":
				req.Revision++
			case "reason":
				req.Reason = ""
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "actor changed":
				calls := 0
				service.Administrator = func(context.Context, string, RepositoryArchivePolicy) (string, error) {
					calls++
					if calls == 1 {
						return "first-administrator", nil
					}
					return "another-administrator", nil
				}
			}
			if _, err := service.Release(ctx, req); err == nil {
				t.Fatal("unbound administrative release")
			}
			if held, err := s.Fence.Held(); held || err != nil {
				t.Fatal("refusal changed global admission", held, err)
			}
			if err := CheckArchiveWrite(native.p.Repository); !errors.Is(err, ErrArchiveReadOnly) {
				t.Fatal("refusal released runtime", err)
			}
		})
	}
}

func TestArchiveReleaseNativeWriteLostReplyAndRefreeze(t *testing.T) {
	f, native := archiveReleaseNativeFixture(t)
	local := filepath.Join(native.wc, "doc.txt")
	if err := os.WriteFile(local, []byte("offline edit survives freeze\n"), 0644); err != nil {
		t.Fatal(err)
	}
	archiveGuardRefused(t, "svn", "commit", "-m", "blocked", native.wc)
	s := f.service()
	got, err := s.Release(t.Context(), f.Request)
	if err != nil || got.State != "released" || got.Epoch != 2 {
		t.Fatal(got, err)
	}
	if err := CheckCanonicalRepositoryWrite(f.Freeze.ServiceWC, f.Request.RepoID); err != nil {
		t.Fatal(err)
	}
	if held, err := s.Fence.Held(); held || err != nil {
		t.Fatal(held, err)
	}
	// A delayed retry of the original freeze is not a new instruction.
	if _, err := f.Freeze.freeze(t.Context()); err == nil {
		t.Fatal("old freeze silently became a new epoch")
	}
	if held, err := s.Fence.Held(); held || err != nil {
		t.Fatal("old freeze retry changed admission", held, err)
	}
	archiveGuardCommand(t, "svn", "commit", "-m", "preserved offline edit", native.wc)
	archiveGuardCommand(t, "svn", "lock", native.url+"/doc.txt", "-m", "ordinary edit after release")
	archiveGuardCommand(t, "svn", "unlock", native.url+"/doc.txt")
	// An old successful reply remains idempotent even after HEAD has advanced.
	spy := &publishRunner{}
	s.Authority.Runner = spy
	if got, err := s.Release(t.Context(), f.Request); err != nil || got.State != "released" || spy.calls != 0 {
		t.Fatal("lost reply", got, spy.calls, err)
	}
	changed := f.Request
	changed.Reason = "a different administrative reason"
	if _, err := s.Release(t.Context(), changed); err == nil {
		t.Fatal("changed reason reused an administrative operation")
	}
	freeze := f.Freeze
	freeze.Plan.OperationID = uuid.NewString()
	freeze.Plan.Revision = 2
	if got, err := freeze.freeze(t.Context()); err != nil || got.State != "archived" || got.Epoch != 3 {
		t.Fatal("refreeze", got, err)
	}
	if _, err := f.service().Release(t.Context(), f.Request); err == nil {
		t.Fatal("old request released newer freeze")
	}
	// Updating the expected epoch does not make an old operation ID reusable.
	reused := f.Request
	reused.ExpectedOperationID, reused.ExpectedEpoch, reused.Revision = freeze.Plan.OperationID, 3, 2
	if _, err := f.service().Release(t.Context(), reused); err == nil {
		t.Fatal("old release operation reused for a newer policy")
	}
	if held, err := s.Fence.Held(); held || err != nil {
		t.Fatal("reused release changed admission", held, err)
	}
	runtime, err := readArchiveProtection(native.p.Repository)
	if err != nil || runtime == nil {
		t.Fatal(runtime, err)
	}
	if err := native.p.Release(t.Context(), *runtime, reused.OperationID, reused.Reason); err == nil {
		t.Fatal("local primitive reused an old release operation")
	}
	archiveGuardRefused(t, "svn", "mkdir", "-m", "still frozen", native.url+"/later")
	auditPath, _ := canonicalArchiveReleasePath(f.Freeze.ServiceWC, f.Request.RepoID, 2)
	if _, err := readCanonicalArchiveRelease(auditPath); err != nil {
		t.Fatal("refreeze erased prior audit", err)
	}
}

func TestArchiveReleaseProcessExit(t *testing.T) {
	name := os.Getenv("FILEES_TEST_ARCHIVE_RELEASE")
	if name == "" {
		return
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var f archiveReleaseProcessFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	archiveReleaseCheckpoint = func(point string) {
		if point == f.Checkpoint {
			os.Exit(72)
		}
	}
	if _, err := f.service().Release(t.Context(), f.Request); err != nil {
		t.Fatal(err)
	}
	t.Fatal("release checkpoint not reached")
}

func TestArchiveReleaseNativeCrashRecovery(t *testing.T) {
	for _, point := range []string{"fenced", "intent", "runtime", "published"} {
		t.Run(point, func(t *testing.T) {
			f, native := archiveReleaseNativeFixture(t)
			f.Checkpoint = point
			fixture := filepath.Join(t.TempDir(), "release-fixture.json")
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestArchiveReleaseProcessExit$")
			cmd.Env = append(os.Environ(), "FILEES_TEST_ARCHIVE_RELEASE="+fixture)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 72 {
				t.Fatalf("checkpoint: %v %s", err, out)
			}
			if held, err := f.service().Fence.Held(); !held || err != nil {
				t.Fatal("crash opened admission", held, err)
			}
			wrong := f.Request
			wrong.OperationID = uuid.NewString()
			if _, err := f.service().Release(t.Context(), wrong); err == nil {
				t.Fatal("another request stole release")
			}
			if err := ReconcileServiceWorkingCopy(t.Context(), f.Freeze.SVN, f.Freeze.ServiceWC); err != nil {
				t.Fatal(err)
			}
			got, err := f.service().Release(t.Context(), f.Request)
			if err != nil || got.State != "released" {
				t.Fatal(got, err)
			}
			if held, err := f.service().Fence.Held(); held || err != nil {
				t.Fatal("unfinished recovery", held, err)
			}
			archiveGuardCommand(t, "svn", "mkdir", "-m", "administratively released", native.url+"/allowed")
		})
	}
}

func TestArchiveReleaseNativePublicationFailureStaysFenced(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(string(rune('0'+at)), func(t *testing.T) {
			f, _ := archiveReleaseNativeFixture(t)
			s := f.service()
			s.Authority.Runner = &archiveFailPublisher{next: s.Authority.Runner, at: at}
			if _, err := s.Release(t.Context(), f.Request); err == nil {
				t.Fatal("failed canonical publication acknowledged")
			}
			if held, err := s.Fence.Held(); !held || err != nil {
				t.Fatal("publication failure opened admission", held, err)
			}
			if err := ReconcileServiceWorkingCopy(t.Context(), f.Freeze.SVN, f.Freeze.ServiceWC); err != nil {
				t.Fatal(err)
			}
			if got, err := f.service().Release(t.Context(), f.Request); err != nil || got.State != "released" {
				t.Fatal(got, err)
			}
		})
	}
}
