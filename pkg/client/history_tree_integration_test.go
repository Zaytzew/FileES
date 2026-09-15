//go:build native_svn_probe && windows

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/svnurl"
)

// The tree adapters against the real helper: a plan that counts an empty
// folder, and a file with svn:eol-style written under a daemon-chosen name with
// the repository's bytes.
func TestHistoryTreeReaderAgainstTheNativeHelper(t *testing.T) {
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
	for _, dir := range []string{filepath.Join(wc, "Docs", "deep"), filepath.Join(wc, "empty")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	want := "line one\nId: $Id$\n"
	if err := os.WriteFile(filepath.Join(wc, "Docs", "deep", "text.txt"), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	run(wc, "add", "Docs", "empty")
	run(wc, "propset", "svn:eol-style", "native", "Docs/deep/text.txt")
	run(wc, "propset", "svn:keywords", "Id", "Docs/deep/text.txt")
	run(wc, "commit", "--username", "owner", "-m", "tree") // r1

	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli.exe")}).(HistoryTreeReader)
	plan := filepath.Join(root, "plan.ndjson")
	summary, err := c.HistoryListTree(t.Context(), url, 1, plan)
	if err != nil || summary != (HistoryTreeSummary{Revision: 1, Dirs: 3, Files: 1, Bytes: int64(len(want))}) {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	kinds := map[string]string{}
	if err := EachHistoryTreeNode(plan, func(node HistoryTreeNode) error { kinds[node.Path] = node.Kind; return nil }); err != nil {
		t.Fatal(err)
	}
	if kinds["empty"] != "dir" || kinds["Docs/deep"] != "dir" || kinds["Docs/deep/text.txt"] != "file" {
		t.Fatalf("plan = %v", kinds)
	}

	dest := filepath.Join(root, "stage")
	if err := os.MkdirAll(filepath.Join(dest, "docs(Docs)"), 0700); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.HistoryFetchTree(t.Context(), url, 1, dest, []HistoryTreePair{{RepoPath: "Docs/deep/text.txt", LocalPath: "docs(Docs)/text.txt"}})
	if err != nil || len(receipt.Files) != 1 || receipt.Files[0].Bytes != int64(len(want)) || len(receipt.Skipped) != 0 {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "docs(Docs)", "text.txt")); err != nil || string(got) != want {
		t.Fatalf("fetched %q, want repository bytes %q (%v)", got, want, err)
	}
}
