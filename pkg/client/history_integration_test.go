//go:build native_svn_probe && windows

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/svnurl"
)

// The adapter against the real helper, with no CLI on the client: a folder a
// reorganisation removed stays listable, and a file with svn:eol-style comes
// back as the repository stores it.
func TestHistoryReaderAgainstTheNativeHelper(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	svn := nativeFixtureTool(t, "svn")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(svn, append([]string{"--non-interactive", "--config-dir", filepath.Join(root, "config")}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("svn %q: %v %s", args, err, out)
		}
	}
	if out, err := exec.Command(nativeFixtureTool(t, "svnadmin"), "create", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	url := svnurl.File(repo)
	run(root, "checkout", url, wc)
	if err := os.MkdirAll(filepath.Join(wc, "OLD", "01_EDITABLES"), 0700); err != nil {
		t.Fatal(err)
	}
	want := "line one\nId: $Id$\n"
	if err := os.WriteFile(filepath.Join(wc, "OLD", "01_EDITABLES", "opis.txt"), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	run(wc, "add", "OLD")
	run(wc, "propset", "svn:eol-style", "native", "OLD/01_EDITABLES/opis.txt")
	run(wc, "propset", "svn:keywords", "Id", "OLD/01_EDITABLES/opis.txt")
	run(wc, "commit", "--username", "owner", "-m", "before") // r1
	run(wc, "delete", "OLD")
	run(wc, "commit", "--username", "owner", "-m", "reorganise") // r2

	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli.exe")}).(HistoryReader)
	if !c.HistoryEnabled() {
		t.Fatal("helper configured but history disabled")
	}
	top, err := c.HistoryList(t.Context(), url, 1)
	if err != nil || len(top) != 1 || top[0].Name != "OLD" || top[0].Kind != "dir" {
		t.Fatalf("r1 root = %+v %v", top, err)
	}
	if now, err := c.HistoryList(t.Context(), url, 2); err != nil || len(now) != 0 {
		t.Fatalf("r2 root = %+v %v", now, err)
	}
	inner, err := c.HistoryList(t.Context(), url+"/OLD/01_EDITABLES", 1)
	if err != nil || len(inner) != 1 || inner[0].Size != int64(len(want)) || inner[0].LastAuthor != "owner" {
		t.Fatalf("removed folder at r1 = %+v %v", inner, err)
	}

	out := filepath.Join(root, "copy.txt")
	n, err := c.HistoryFetchFile(t.Context(), url+"/OLD/01_EDITABLES/opis.txt", 1, out)
	if err != nil || n != int64(len(want)) {
		t.Fatalf("fetch n=%d err=%v", n, err)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != want {
		t.Fatalf("fetched %q, want repository bytes %q (%v)", got, want, err)
	}
	if _, err := c.HistoryFetchFile(t.Context(), url+"/OLD/01_EDITABLES/opis.txt", 2, filepath.Join(root, "gone.txt")); err == nil {
		t.Fatal("fetched a path that did not exist at r2")
	}
}
