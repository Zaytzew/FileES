//go:build native_svn_probe && (windows || linux)

package client

import (
	"filees/internal/svnurl"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeTokenBoundUnlock(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	run := func(tool string, args ...string) string {
		t.Helper()
		cmd := exec.Command(nativeFixtureTool(t, tool), args...)
		cmd.Dir = root // never let native argv globbing reach the source WC
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return string(out)
	}
	run("svnadmin", "create", repo)
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent.exe")}).(*execClient)
	url := svnurl.File(repo)
	if _, err := c.Checkout(t.Context(), url, wc); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(wc, "test-ą.txt")
	if err := os.WriteFile(p, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(t.Context(), wc, []string{p}); err != nil {
		t.Fatal(err)
	}
	run("svn", "propset", "svn:needs-lock", "yes", p)
	if _, _, err := c.CommitWithRevision(t.Context(), wc, url, []string{p}, "fixture", false); err != nil {
		t.Fatal(err)
	}
	acquire := func() string {
		t.Helper()
		proof, _, err := c.AcquireLock(t.Context(), wc, p, "test intent")
		if err != nil || proof == nil {
			t.Fatal(proof, err)
		}
		return proof.Token
	}
	token := acquire()
	if _, _, err := c.ReleaseLock(t.Context(), wc, p, "wrong-token"); err == nil {
		t.Fatal("wrong local token accepted")
	}
	for i := 0; i < 2; i++ {
		proof, out, err := c.ReleaseLock(t.Context(), wc, p, token)
		if err != nil || proof == nil {
			t.Fatal(proof, err)
		}
		if strings.Contains(out, token) {
			t.Fatal("token leaked into output")
		}
		if i == 0 && proof.State != "released" || i == 1 && proof.State != "not-held" {
			t.Fatal(proof)
		}
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm()&0200 != 0 {
		t.Fatal("needs-lock readonly not restored", info, err)
	}
	token = acquire()
	// Simulate the owner losing its lock to a later holder. No local token update.
	run("svn", "lock", "--force", "-m", "new holder", url+"/test-%C4%85.txt")
	before := run("svnadmin", "lslocks", repo)
	proof, _, err := c.ReleaseLock(t.Context(), wc, p, token)
	if err != nil || proof == nil || proof.State != "not-held" {
		t.Fatal(proof, err)
	}
	if after := run("svnadmin", "lslocks", repo); after != before {
		t.Fatal("replacement lock modified")
	}
	if data, err := os.ReadFile(p); err != nil || string(data) != "unchanged" {
		t.Fatal("document changed", err)
	}
}
