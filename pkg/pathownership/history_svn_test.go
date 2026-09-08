package pathownership

import (
	"filees/internal/svnurl"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommittedSVNObjectHistory(t *testing.T) {
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	run := func(binary string, args ...string) []byte {
		t.Helper()
		out, err := exec.Command(binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", binary, args, err, out)
		}
		return out
	}
	run("svnadmin", "create", repo)
	run("svn", "checkout", svnurl.File(repo), wc)
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(wc, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("same content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(author string) {
		t.Helper()
		run("svn", "commit", "--username", author, "-m", "history", wc)
		run("svn", "update", wc)
	}
	snapshot := func(head int64) Snapshot {
		t.Helper()
		raw := run("svn", "log", "--xml", "--verbose", "--quiet", svnurl.File(repo))
		log, err := ParseLog(raw)
		if err != nil {
			t.Fatal(err)
		}
		s, err := Replay(t.Context(), "repo", head, log)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	write("a/doc")
	write("a/delete")
	write("a/sub/old")
	run("svn", "add", filepath.Join(wc, "a"))
	commit("creator")
	first := snapshot(1)
	run("svn", "move", filepath.Join(wc, "a"), filepath.Join(wc, "renamed"))
	commit("administrator")
	moved := snapshot(2)
	for i := range first.Entries {
		if first.Entries[i].Object != moved.Entries[i].Object {
			t.Fatal("directory rename changed creator")
		}
	}
	run("svn", "copy", filepath.Join(wc, "renamed"), filepath.Join(wc, "fork"))
	run("svn", "delete", filepath.Join(wc, "fork/delete"))
	run("svn", "delete", filepath.Join(wc, "fork/sub"))
	write("fork/sub/new")
	run("svn", "add", filepath.Join(wc, "fork/sub"))
	commit("forker")
	fork := snapshot(3)
	run("svn", "delete", filepath.Join(wc, "renamed"))
	commit("cleaner")
	after := snapshot(4)
	if len(after.Entries) != 2 {
		t.Fatalf("resurrected children: %+v", after)
	}
	for i, e := range after.Entries {
		if e.FirstCommitter != "forker" || e.CreatedRevision != 3 || e.Object != fork.Entries[i].Object {
			t.Fatalf("fork history changed on later source delete: %+v", e)
		}
	}
	// Reusing the old name with identical bytes does not resurrect the object.
	write("renamed/doc")
	run("svn", "add", filepath.Join(wc, "renamed"))
	commit("new-creator")
	last := snapshot(5)
	if last.Entries[2].FirstCommitter != "new-creator" || last.Entries[2].ID == first.Entries[1].ID {
		t.Fatal("re-add inherited old ownership")
	}
}
