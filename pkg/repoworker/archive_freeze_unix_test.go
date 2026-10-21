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

	"filees/internal/svnurl"
	"github.com/google/uuid"
)

type archiveFreezeProcessFixture struct {
	ServiceWC, Authz, FenceRoot, Repository, Executable, SVN, SVNLook, SVNAdmin, Realm, Checkpoint string
	Plan                                                                                           ArchiveProtectionRecord
}

func (f archiveFreezeProcessFixture) service() ArchiveFreezeService {
	return ArchiveFreezeService{
		Authority:  ServicePublisher{ServiceWC: f.ServiceWC, DataAuthzFile: f.Authz, Runner: SVNPublishRunner{SVN: f.SVN, WorkingCopy: f.ServiceWC}},
		Protection: ArchiveProtection{Repository: f.Repository, Executable: f.Executable, SVNLook: f.SVNLook, SVNAdmin: f.SVNAdmin},
		Fence:      ArchiveAdmissionFence{Root: f.FenceRoot},
		// Real OS session draining is separately exercised by servertool's
		// adapter test. Here we terminate the coordinator itself at boundaries.
		WithSessionsStopped: func(_ context.Context, fn func() error) error { return fn() },
	}
}

func (f archiveFreezeProcessFixture) freeze(ctx context.Context) (RepositoryArchivePolicy, error) {
	return f.service().Freeze(ctx, f.Realm, f.Plan.RepoID, f.Plan.OperationID, f.Plan.RepositoryUUID, f.Plan.Revision)
}

func archiveFreezeNativeFixture(t *testing.T) (archiveFreezeProcessFixture, archiveGuardNative) {
	t.Helper()
	native := archiveGuardNativeFixture(t)
	p, r, _, _ := archivePolicyFixture(t)
	path, _ := repositoryRecordPath(p.ServiceWC, r.RepoID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r.RepoID = native.plan.RepoID
	path, _ = repositoryRecordPath(p.ServiceWC, r.RepoID)
	if err := atomicJSON(path, r); err != nil {
		t.Fatal(err)
	}
	// Remove the old grant from this policy-only fixture.
	if err := os.RemoveAll(filepath.Join(p.ServiceWC, "admin", "grants")); err != nil {
		t.Fatal(err)
	}
	serviceRepo := filepath.Join(t.TempDir(), "service-repo")
	archiveGuardCommand(t, "svnadmin", "create", serviceRepo)
	archiveGuardCommand(t, "svn", "import", "-m", "canonical fixture", p.ServiceWC, svnurl.File(serviceRepo))
	wc := filepath.Join(t.TempDir(), "service-wc")
	archiveGuardCommand(t, "svn", "checkout", svnurl.File(serviceRepo), wc)
	root := t.TempDir()
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Fatal(err)
	}
	return archiveFreezeProcessFixture{ServiceWC: wc, Authz: filepath.Join(wc, "data.authz"), FenceRoot: root, Repository: native.p.Repository, Executable: native.p.Executable, SVN: svn, SVNLook: native.p.SVNLook, SVNAdmin: native.p.SVNAdmin, Realm: r.OwnerRealmID, Plan: native.plan}, native
}

func TestArchiveFreezeProcessExit(t *testing.T) {
	name := os.Getenv("FILEES_TEST_ARCHIVE_FREEZE")
	if name == "" {
		return
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var f archiveFreezeProcessFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	archiveFreezeCheckpoint = func(point string) {
		if point == f.Checkpoint {
			os.Exit(71)
		}
	}
	if _, err := f.freeze(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("checkpoint not reached")
}

func TestArchiveFreezeNativeCrashRecovery(t *testing.T) {
	for _, point := range []string{"fenced", "intent", "guarded", "published"} {
		t.Run(point, func(t *testing.T) {
			f, native := archiveFreezeNativeFixture(t)
			f.Checkpoint = point
			fixture := filepath.Join(t.TempDir(), "fixture.json")
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestArchiveFreezeProcessExit$")
			cmd.Env = append(os.Environ(), "FILEES_TEST_ARCHIVE_FREEZE="+fixture)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 71 {
				t.Fatalf("checkpoint: %v %s", err, out)
			}
			if held, err := f.service().Fence.Held(); !held || err != nil {
				t.Fatal("crash opened admission", held, err)
			}
			wrong := f
			wrong.Plan.OperationID = uuid.NewString()
			if _, err := wrong.freeze(t.Context()); err == nil {
				t.Fatal("another operation stole transition")
			}
			// Same reconciliation used on a fresh server invocation.
			if err := ReconcileServiceWorkingCopy(t.Context(), f.SVN, f.ServiceWC); err != nil {
				t.Fatal(err)
			}
			got, err := f.freeze(t.Context())
			if err != nil || got.State != "archived" {
				t.Fatal(got, err)
			}
			if held, err := f.service().Fence.Held(); held || err != nil {
				t.Fatal("completed fence", held, err)
			}
			if err := CheckCanonicalRepositoryWrite(f.ServiceWC, f.Plan.RepoID); !errors.Is(err, ErrArchiveReadOnly) {
				t.Fatal(err)
			}
			archiveGuardRefused(t, "svn", "mkdir", "-m", "forbidden", native.url+"/new")
			if got := string(archiveGuardCommand(t, "svnlook", "youngest", native.p.Repository)); got != "1\n" {
				t.Fatal(got)
			}
		})
	}
}

func TestArchiveFreezeNativeLocksAndStalePlan(t *testing.T) {
	f, native := archiveFreezeNativeFixture(t)
	wrong := f
	wrong.Realm = uuid.NewString()
	if _, err := wrong.freeze(t.Context()); err == nil {
		t.Fatal("foreign realm")
	}
	wrong = f
	wrong.Plan.Revision++
	if _, err := wrong.freeze(t.Context()); err == nil {
		t.Fatal("stale plan")
	}
	if held, err := f.service().Fence.Held(); held || err != nil {
		t.Fatal("invalid plan had effects", held, err)
	}
	archiveGuardCommand(t, "svn", "lock", native.url+"/doc.txt", "-m", "existing edit")
	got, err := f.freeze(t.Context())
	if !errors.Is(err, ErrArchiveLocks) || got.State != "freezing" {
		t.Fatal(got, err)
	}
	if held, err := f.service().Fence.Held(); held || err != nil {
		t.Fatal("guarded unlock still held", held, err)
	}
	archiveGuardRefused(t, "svn", "mkdir", "-m", "forbidden", native.url+"/new")
	archiveGuardCommand(t, "svn", "unlock", native.url+"/doc.txt")
	got, err = f.freeze(t.Context())
	if err != nil || got.State != "archived" {
		t.Fatal(got, err)
	}
}

type archiveFailPublisher struct {
	next      PublishRunner
	at, calls int
}

func (r *archiveFailPublisher) Publish(ctx context.Context, paths []string, message string) error {
	r.calls++
	if r.calls == r.at {
		return errors.New("injected canonical publication failure")
	}
	return r.next.Publish(ctx, paths, message)
}

func TestArchiveFreezeNativePublicationFailureStaysFenced(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(string(rune('0'+at)), func(t *testing.T) {
			f, _ := archiveFreezeNativeFixture(t)
			s := f.service()
			s.Authority.Runner = &archiveFailPublisher{next: s.Authority.Runner, at: at}
			if _, err := s.Freeze(t.Context(), f.Realm, f.Plan.RepoID, f.Plan.OperationID, f.Plan.RepositoryUUID, f.Plan.Revision); err == nil {
				t.Fatal("failed publication acknowledged")
			}
			if held, err := s.Fence.Held(); !held || err != nil {
				t.Fatal("failed publication reopened", held, err)
			}
			if err := ReconcileServiceWorkingCopy(t.Context(), f.SVN, f.ServiceWC); err != nil {
				t.Fatal(err)
			}
			if got, err := f.freeze(t.Context()); err != nil || got.State != "archived" {
				t.Fatal(got, err)
			}
		})
	}
}
