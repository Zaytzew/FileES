//go:build native_svn_probe && (windows || linux)

package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/svnurl"
)

// The status the helper reports for a file that was new - never added - and
// was moved into a new folder before its first commit. The commit service
// decides from exactly these two answers whether the move is a rename to
// publish or simply a new file, so they are pinned here against the real
// helper rather than assumed. Owner's desktop, 2026-09-23: a photo moved into
// 01_PROJEKT-WNĘTRZ/23.09.2026 held publication every 30 s.
func TestNativeStatusOfANewFileMovedBeforeItsFirstCommit(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	svn := nativeFixtureTool(t, "svn")
	run := func(tool string, args ...string) {
		t.Helper()
		if out, err := exec.Command(tool, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", tool, args, err, out)
		}
	}
	run(nativeFixtureTool(t, "svnadmin"), "create", repo)
	run(svn, "checkout", "--quiet", svnurl.File(repo), wc)
	folder := filepath.Join(wc, "01_PROJEKT-WNĘTRZ")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	run(svn, "add", "--quiet", folder)
	run(svn, "commit", "--quiet", "-m", "base", wc)

	name := "HOTE_14_ŁAZIENKA  w pokoju.jpg"
	oldRel := "01_PROJEKT-WNĘTRZ/" + name
	newRel := "01_PROJEKT-WNĘTRZ/23.09.2026/" + name
	if err := os.WriteFile(filepath.Join(wc, filepath.FromSlash(oldRel)), []byte("photo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(folder, "23.09.2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(wc, filepath.FromSlash(oldRel)), filepath.Join(wc, filepath.FromSlash(newRel))); err != nil {
		t.Fatal(err)
	}

	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli")}).(*execClient)
	entries, err := c.Status(context.Background(), wc, []string{oldRel, newRel})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	items := map[string]string{}
	for _, e := range entries {
		items[filepath.ToSlash(e.Path)] = e.Item
	}
	// The vanished source is reported explicitly as "none": gone from disk and
	// unknown to the working copy. The svn CLI simply did not list such a path,
	// and pkg/commit was written for that, so it must read "none" as absent.
	if got := items[oldRel]; got != "none" {
		t.Fatalf("source status = %q, want none (all: %v)", got, items)
	}
	// Inside a new, unversioned folder the file itself is reported, and as
	// unversioned - not left out the way the CLI leaves children of "?".
	if got := items[newRel]; got != "unversioned" {
		t.Fatalf("destination status = %q, want unversioned (all: %v)", got, items)
	}
}
