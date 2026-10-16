package repoworker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
)

func TestHistoryCacheRejectsSameUUIDAndHEADReplacement(t *testing.T) {
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	admin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	root := t.TempDir()
	run := func(tool string, args ...string) string {
		t.Helper()
		cmd := exec.Command(tool, args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	id := "7bbe75ae-5d05-4442-a513-5b53f014e53a"
	repo := filepath.Join(root, id)
	create := func(path, name string) {
		run(admin, "create", path)
		wc := filepath.Join(root, "wc-"+name)
		run(svn, "co", svnurl.File(path), wc)
		if err := os.WriteFile(filepath.Join(wc, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		run(svn, "add", filepath.Join(wc, name))
		run(svn, "ci", "--username", "same-author", "-m", "fixture", wc)
	}
	create(repo, "old-object")
	source := SVNPathOwners{SVN: svn, RepositoriesRoot: root, ServiceWC: root, CacheRoot: filepath.Join(root, "cache")}
	if err := os.Mkdir(source.CacheRoot, 0700); err != nil {
		t.Fatal(err)
	}
	read := func() (RepositoryRevision, string) {
		t.Helper()
		head, err := source.Head(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := source.history(t.Context(), id, svnurl.File(repo), head)
		if err != nil || len(snapshot.Entries) != 1 {
			t.Fatal(snapshot, err)
		}
		return head, snapshot.Entries[0].Path
	}
	before, name := read()
	if name != "old-object" {
		t.Fatal(name)
	}
	if err := os.Rename(repo, repo+"-retained"); err != nil {
		t.Fatal(err)
	}
	create(repo, "restored-object")
	run(admin, "setuuid", repo, before.UUID)
	after, name := read()
	if after.UUID != before.UUID || after.Number != before.Number || after.HistoryIdentity == before.HistoryIdentity || name != "restored-object" {
		t.Fatal("old history reused for restored repository", before, after, name)
	}
	// Even copying the whole UUID/instance record must not identify a new
	// physical root as the cached old repository (e.g. administrative restore).
	raw, err := os.ReadFile(filepath.Join(repo+"-retained", "db", "uuid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "db", "uuid"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ident, err := repositoryHistoryIdentity(repo, before.UUID)
	if err != nil || ident == before.HistoryIdentity {
		t.Fatal(ident, err)
	}
}
