//go:build !windows

package repoworker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
	"github.com/google/uuid"
)

type archiveGuardNative struct {
	p       ArchiveProtection
	plan    ArchiveProtectionRecord
	wc, url string
}

func archiveGuardCommand(t *testing.T, tool string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tool, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %q: %v %s", tool, args, err, out)
	}
	return out
}

func archiveGuardNativeFixture(t *testing.T) archiveGuardNative {
	t.Helper()
	tools := map[string]string{}
	for _, name := range []string{"svn", "svnadmin", "svnlook", "svnmucc"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skip(name + " unavailable")
		}
		tools[name] = path
	}
	repo := filepath.Join(t.TempDir(), uuid.NewString())
	archiveGuardCommand(t, tools["svnadmin"], "create", repo)
	u := svnurl.File(repo)
	wc := filepath.Join(t.TempDir(), "wc")
	archiveGuardCommand(t, tools["svn"], "checkout", "--non-interactive", u, wc)
	if err := os.WriteFile(filepath.Join(wc, "doc.txt"), []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	archiveGuardCommand(t, tools["svn"], "add", filepath.Join(wc, "doc.txt"))
	archiveGuardCommand(t, tools["svn"], "commit", "-m", "seed", wc)
	worker := os.Getenv("FILEES_ARCHIVE_WORKER")
	if worker == "" {
		worker = os.Args[0]
	}
	p := ArchiveProtection{Repository: repo, Executable: worker, SVNLook: tools["svnlook"], SVNAdmin: tools["svnadmin"], WithSessionsStopped: func(ctx context.Context, fn func() error) error { return fn() }, Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }}
	id := strings.TrimSpace(string(archiveGuardCommand(t, tools["svnlook"], "uuid", repo)))
	plan := ArchiveProtectionRecord{RepoID: filepath.Base(repo), RepositoryUUID: id, OperationID: uuid.NewString(), Epoch: 1, Revision: 1}
	return archiveGuardNative{p: p, plan: plan, wc: wc, url: u}
}

func archiveGuardRefused(t *testing.T, tool string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tool, args...).CombinedOutput()
	if err == nil || (!bytes.Contains(out, []byte("archive")) && !bytes.Contains(out, []byte("administrative"))) {
		t.Fatalf("expected archive refusal: %s %q: %v %s", tool, args, err, out)
	}
}

func TestArchiveProtectionNativeFreezeReleaseAndQuota(t *testing.T) {
	f := archiveGuardNativeFixture(t)
	if err := InstallRealmQuota(f.p.Repository, f.p.Executable, uuid.NewString(), 1<<30); err != nil {
		t.Fatal(err)
	}
	// An offline edit predates the policy transition and must not be lost.
	local := []byte("unpublished local edit\n")
	if err := os.WriteFile(filepath.Join(f.wc, "doc.txt"), local, 0644); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Freeze(t.Context(), f.plan); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"commit", "-m", "offline edit", f.wc},
		{"delete", f.url + "/doc.txt", "-m", "delete"},
		{"move", f.url + "/doc.txt", f.url + "/renamed.txt", "-m", "rename"},
		{"mkdir", f.url + "/new-child", "-m", "ordinary append"},
		{"propset", "--revprop", "-r", "1", "svn:log", "changed", f.url},
		{"lock", f.url + "/doc.txt", "-m", "new lock"},
	} {
		archiveGuardRefused(t, "svn", args...)
	}
	archiveGuardRefused(t, "svnmucc", "-m", "property edit", "propset", "example:property", "changed", f.url+"/doc.txt")
	archiveGuardCommand(t, "svn", "list", f.url)
	if got := archiveGuardCommand(t, "svn", "cat", f.url+"/doc.txt"); string(got) != "initial\n" {
		t.Fatal("archive bytes changed")
	}
	if got, err := os.ReadFile(filepath.Join(f.wc, "doc.txt")); err != nil || !bytes.Equal(got, local) {
		t.Fatal("offline edit lost", err)
	}
	if err := f.p.Freeze(t.Context(), f.plan); err != nil {
		t.Fatal("idempotent freeze", err)
	}
	r, err := readArchiveProtection(f.p.Repository)
	if err != nil || r == nil || r.State != "archived" {
		t.Fatal(r, err)
	}
	if err := f.p.Release(t.Context(), *r, uuid.NewString(), ""); err == nil {
		t.Fatal("unaudited release")
	}
	if !errors.Is(CheckArchiveWrite(f.p.Repository), ErrArchiveReadOnly) {
		t.Fatal("failed release opened writes")
	}
	releaseID := uuid.NewString()
	if err := f.p.Release(t.Context(), *r, releaseID, "isolated administrator test"); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Release(t.Context(), *r, releaseID, "isolated administrator test"); err != nil {
		t.Fatal("lost release response", err)
	}
	if err := f.p.Release(t.Context(), *r, uuid.NewString(), "another request"); err == nil {
		t.Fatal("adopted another release")
	}
	archiveGuardCommand(t, "svn", "commit", "-m", "explicitly released", f.wc)
	// Quota must still execute after administrative release, not be bypassed.
	marker := filepath.Join(f.p.Repository, "hooks", realmQuotaMarkerName)
	quota, err := readRealmQuotaMarker(marker)
	if err != nil {
		t.Fatal(err)
	}
	quota.QuotaBytes = 1
	if err := atomicJSON(marker, quota); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("svn", "mkdir", f.url+"/over-quota", "-m", "quota").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("demo account is limited")) {
		t.Fatalf("quota lost: %v %s", err, out)
	}
	archiveGuardRefused(t, "svn", "propset", "--revprop", "-r", "1", "svn:log", "changed", f.url)
	// Refreeze is a new, auditable epoch, never a reset of the first one.
	refreeze := f.plan
	refreeze.Revision, refreeze.Epoch, refreeze.OperationID = 2, 3, uuid.NewString()
	if err := f.p.Freeze(t.Context(), refreeze); err != nil {
		t.Fatal("refreeze", err)
	}
	archiveGuardRefused(t, "svn", "mkdir", f.url+"/refrozen", "-m", "blocked")
}

func TestArchiveProtectionNativeWaitForLockAndResume(t *testing.T) {
	f := archiveGuardNativeFixture(t)
	archiveGuardCommand(t, "svn", "lock", filepath.Join(f.wc, "doc.txt"), "-m", "existing lock")
	before := archiveGuardCommand(t, f.p.SVNAdmin, "lslocks", f.p.Repository)
	if err := f.p.Freeze(t.Context(), f.plan); !errors.Is(err, ErrArchiveLocks) {
		t.Fatal("did not wait for lock", err)
	}
	if after := archiveGuardCommand(t, f.p.SVNAdmin, "lslocks", f.p.Repository); !bytes.Equal(before, after) {
		t.Fatal("freeze changed lock")
	}
	archiveGuardRefused(t, "svn", "mkdir", f.url+"/while-freezing", "-m", "blocked")
	archiveGuardCommand(t, "svn", "unlock", filepath.Join(f.wc, "doc.txt"))
	if err := f.p.Freeze(t.Context(), f.plan); err != nil {
		t.Fatal("resume", err)
	}
	if got := strings.TrimSpace(string(archiveGuardCommand(t, f.p.SVNLook, "youngest", f.p.Repository))); got != "1" {
		t.Fatal("freeze created a commit", got)
	}
}

func TestArchiveProtectionNativeForeignHooksCancellationAndPartialInstall(t *testing.T) {
	for _, name := range archiveGuardHooks() {
		t.Run(name, func(t *testing.T) {
			f := archiveGuardNativeFixture(t)
			hook := filepath.Join(f.p.Repository, "hooks", name)
			body := []byte("#!/bin/sh\nexit 0\n")
			if err := os.WriteFile(hook, body, 0755); err != nil {
				t.Fatal(err)
			}
			if err := f.p.Freeze(t.Context(), f.plan); err == nil || !strings.Contains(err.Error(), "explicit hook integration") {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(hook); err != nil || !bytes.Equal(got, body) {
				t.Fatal("foreign hook changed")
			}
			if _, err := os.Lstat(filepath.Join(f.p.Repository, "hooks", archiveGuardDirectory)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refusal created policy", err)
			}
		})
	}
	f := archiveGuardNativeFixture(t)
	f.p.WithSessionsStopped = func(ctx context.Context, fn func() error) error { return context.DeadlineExceeded }
	if err := f.p.Freeze(t.Context(), f.plan); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := CheckArchiveWrite(f.p.Repository); err != nil {
		t.Fatal("unacknowledged barrier changed policy", err)
	}
	f.p.WithSessionsStopped = func(ctx context.Context, fn func() error) error { return fn() }
	hooks := filepath.Join(f.p.Repository, "hooks")
	root := filepath.Join(hooks, archiveGuardDirectory)
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	r := f.plan
	r.Schema, r.State, r.At = ArchiveGuardVersion, "freezing", f.p.Now()
	if err := writeArchiveProtection(root, r); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.p.Executable, filepath.Join(hooks, "pre-commit")); err != nil {
		t.Fatal(err)
	}
	// Reconstruct from disk, not an in-memory flag, as after process exit.
	resumed := f.p
	if err := resumed.Freeze(t.Context(), f.plan); err != nil {
		t.Fatal("partial install", err)
	}
	stale := f.plan
	stale.OperationID = uuid.NewString()
	if err := resumed.Freeze(t.Context(), stale); err == nil {
		t.Fatal("foreign transition adopted")
	}
	// Model a process stopping after a released audit event has been fsynced,
	// but before switching the active pointer. A retry keeps the event's time.
	current, err := readArchiveProtection(f.p.Repository)
	if err != nil || current == nil {
		t.Fatal(err)
	}
	releaseID := uuid.NewString()
	if err := resumed.Release(t.Context(), *current, releaseID, "interrupted release"); err != nil {
		t.Fatal(err)
	}
	released, err := os.ReadFile(filepath.Join(root, archiveGuardRecord))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(filepath.Join(root, "00000000000000000001-archived.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, archiveGuardRecord), prior, 0644); err != nil {
		t.Fatal(err)
	}
	resumed.Now = func() time.Time { return time.Unix(1700000100, 0).UTC() }
	if err := resumed.Release(t.Context(), *current, releaseID, "interrupted release"); err != nil {
		t.Fatal("release recovery", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, archiveGuardRecord)); err != nil || !bytes.Equal(got, released) {
		t.Fatal("release changed audit on recovery")
	}
	if err := os.Remove(filepath.Join(root, archiveGuardRecord)); err != nil {
		t.Fatal(err)
	}
	archiveGuardRefused(t, "svn", "mkdir", f.url+"/missing-policy", "-m", "blocked")
}
