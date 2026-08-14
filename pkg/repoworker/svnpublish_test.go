package repoworker

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func testFileURL(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func TestSVNPublishRunnerCommitsAuthorityFiles(t *testing.T) {
	svn, e := exec.LookPath("svn")
	if e != nil {
		t.Skip("svn unavailable")
	}
	admin, e := exec.LookPath("svnadmin")
	if e != nil {
		t.Skip("svnadmin unavailable")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	if out, e := exec.Command(admin, "create", repo).CombinedOutput(); e != nil {
		t.Fatalf("svnadmin: %v %s", e, out)
	}
	if out, e := exec.Command(svn, "checkout", testFileURL(repo), wc).CombinedOutput(); e != nil {
		t.Fatalf("checkout: %v %s", e, out)
	}
	path := filepath.Join(wc, "admin", "repository.json")
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("{}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	r := SVNPublishRunner{SVN: svn, WorkingCopy: wc}
	if e = r.Publish(context.Background(), []string{path}, "test authority"); e != nil {
		t.Fatal(e)
	}
	out, e := exec.Command(svn, "log", "-l", "1", path).CombinedOutput()
	if e != nil || !bytes.Contains(out, []byte("test authority")) {
		t.Fatalf("log: %v %s", e, out)
	}
}

func TestSVNPublishRunnerDoesNotAbsorbUnrelatedWorkingCopyChanges(t *testing.T) {
	svn, e := exec.LookPath("svn")
	if e != nil {
		t.Skip("svn unavailable")
	}
	admin, e := exec.LookPath("svnadmin")
	if e != nil {
		t.Skip("svnadmin unavailable")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	if out, e := exec.Command(admin, "create", repo).CombinedOutput(); e != nil {
		t.Fatalf("svnadmin: %v %s", e, out)
	}
	if out, e := exec.Command(svn, "checkout", testFileURL(repo), wc).CombinedOutput(); e != nil {
		t.Fatalf("checkout: %v %s", e, out)
	}
	wanted := filepath.Join(wc, "admin", "wanted.json")
	unrelated := filepath.Join(wc, "admin", "leftover.json")
	if e = os.MkdirAll(filepath.Dir(wanted), 0o700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(wanted, []byte("{}\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(unrelated, []byte("poison\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	r := SVNPublishRunner{SVN: svn, WorkingCopy: wc}
	if e = r.Publish(context.Background(), []string{wanted}, "scoped authority"); e != nil {
		t.Fatal(e)
	}
	out, e := exec.Command(svn, "status", unrelated).CombinedOutput()
	if e != nil || !bytes.HasPrefix(out, []byte("?")) {
		t.Fatalf("unrelated status: %v %q", e, out)
	}
	if out, e = exec.Command(svn, "ls", testFileURL(filepath.Join(repo, "admin", "leftover.json"))).CombinedOutput(); e == nil {
		t.Fatalf("unrelated file was committed: %s", out)
	}
}

func TestReconcileServiceWorkingCopyRemovesInterruptedTransactionState(t *testing.T) {
	svn, e := exec.LookPath("svn")
	if e != nil {
		t.Skip("svn unavailable")
	}
	admin, e := exec.LookPath("svnadmin")
	if e != nil {
		t.Skip("svnadmin unavailable")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	if out, e := exec.Command(admin, "create", repo).CombinedOutput(); e != nil {
		t.Fatalf("svnadmin: %v %s", e, out)
	}
	if out, e := exec.Command(svn, "checkout", testFileURL(repo), wc).CombinedOutput(); e != nil {
		t.Fatalf("checkout: %v %s", e, out)
	}
	tracked := filepath.Join(wc, "admin", "tracked.json")
	orphan := filepath.Join(wc, "admin", "orphan.json")
	if e = os.MkdirAll(filepath.Dir(tracked), 0o700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(tracked, []byte("canonical\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	r := SVNPublishRunner{SVN: svn, WorkingCopy: wc}
	if e = r.Publish(context.Background(), []string{tracked}, "baseline"); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(tracked, []byte("dirty\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(orphan, []byte("partial\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if out, addErr := exec.Command(svn, "add", "--parents", orphan).CombinedOutput(); addErr != nil {
		t.Fatalf("schedule interrupted orphan: %v %s", addErr, out)
	}
	if e = ReconcileServiceWorkingCopy(context.Background(), svn, wc); e != nil {
		t.Fatal(e)
	}
	content, e := os.ReadFile(tracked)
	if e != nil || string(content) != "canonical\n" {
		t.Fatalf("tracked=%q err=%v", content, e)
	}
	if _, e = os.Stat(orphan); !os.IsNotExist(e) {
		t.Fatalf("orphan still exists: %v", e)
	}
	if out, e := exec.Command(svn, "status", wc).CombinedOutput(); e != nil || len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("working copy not clean: %v %q", e, out)
	}
}
