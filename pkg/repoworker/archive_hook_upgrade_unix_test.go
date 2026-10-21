//go:build !windows

package repoworker

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArchiveHookSymlinkFollowsAtomicWorkerReplacement(t *testing.T) {
	f := archiveGuardNativeFixture(t)
	original := f.p.Executable
	worker := filepath.Join(t.TempDir(), "filees-worker")
	copyWorker := func(target string) {
		t.Helper()
		in, err := os.Open(original)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			t.Fatal(err)
		}
		if err := out.Sync(); err != nil {
			out.Close()
			t.Fatal(err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
	}
	copyWorker(worker)
	f.p.Executable = worker
	if err := f.p.Freeze(t.Context(), f.plan); err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(worker)
	if err != nil {
		t.Fatal(err)
	}
	copyWorker(worker + ".next")
	if err := os.Rename(worker+".next", worker); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(filepath.Dir(worker)); err != nil {
		t.Fatal(err)
	}
	next, err := os.Stat(worker)
	if err != nil || os.SameFile(old, next) {
		t.Fatal("worker inode was not replaced", err)
	}
	for _, name := range archiveGuardHooks() {
		hook := filepath.Join(f.p.Repository, "hooks", name)
		link, err := os.Readlink(hook)
		if err != nil || link != worker {
			t.Fatal("hook was relinked or is not a symlink", name, err)
		}
		resolved, err := os.Stat(hook)
		if err != nil || !os.SameFile(next, resolved) {
			t.Fatal("hook still resolves to old image", name, err)
		}
	}
	hook := filepath.Join(f.p.Repository, "hooks", "pre-commit")
	out, err := exec.CommandContext(t.Context(), hook, f.p.Repository, "unpublished-transaction").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("archive")) {
		t.Fatalf("replacement worker did not enforce archive guard: %v %s", err, out)
	}
	archiveGuardRefused(t, "svn", "mkdir", f.url+"/denied-after-upgrade", "-m", "must remain archived")
	if err := f.p.Freeze(t.Context(), f.plan); err != nil {
		t.Fatal("unchanged hook binding refused new worker", err)
	}
	// This proves next-exec behavior only. No claim about already running
	// sessions, resident services or permission to update a protected server.
}
