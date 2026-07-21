package repoworker

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
	if out, e := exec.Command(svn, "checkout", "file://"+repo, wc).CombinedOutput(); e != nil {
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
