package repoworker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"filees/internal/svnurl"
	"filees/pkg/pathownership"
)

func TestHistoryAuthorsRejectIncompleteAndKeepObjectIdentity(t *testing.T) {
	cached := pathownership.Snapshot{Revision: 2, RepositoryUUID: "incarnation", Entries: []pathownership.Entry{{Path: "renamed", Object: pathownership.Object{ID: "stable", CreatedRevision: 1, FirstCommitter: "old"}}}}
	for _, revs := range [][]pathownership.Revision{nil, {{Number: 1}}, {{Number: 1}, {Number: 1}}, {{Number: 0}, {Number: 2}}, {{Number: 1}, {Number: 3}}} {
		if _, _, err := rebindHistoryAuthors(t.Context(), cached, revs); err == nil {
			t.Fatal("incomplete authors accepted", revs)
		}
	}
	revs := []pathownership.Revision{{Number: 2, Author: "renamer"}, {Number: 1, Author: "new"}}
	got, changed, err := rebindHistoryAuthors(t.Context(), cached, revs)
	if err != nil || !changed || got.Entries[0].FirstCommitter != "new" || got.Entries[0].ID != "stable" || cached.Entries[0].FirstCommitter != "old" {
		t.Fatal(got, changed, err)
	}
	if _, changed, err = rebindHistoryAuthors(t.Context(), got, revs); err != nil || changed {
		t.Fatal(changed, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err = rebindHistoryAuthors(ctx, cached, revs); err == nil {
		t.Fatal("cancel ignored")
	}
	cached.Entries[0].CreatedRevision = 3
	if _, _, err = rebindHistoryAuthors(t.Context(), cached, revs); err == nil {
		t.Fatal("invalid origin accepted")
	}
}

func TestSVNHistoryCacheSeesRevpropChangeWithoutCommit(t *testing.T) {
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	admin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	run := func(tool string, args ...string) {
		t.Helper()
		cmd := exec.Command(tool, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	run(admin, "create", repo)
	url := svnurl.File(repo)
	run(svn, "checkout", url, wc)
	file := filepath.Join(wc, "sample")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	run(svn, "add", file)
	run(svn, "commit", "--username", "before", "-m", "fixture", wc)
	source := SVNPathOwners{SVN: svn, CacheRoot: filepath.Join(root, "cache")}
	if err := os.Mkdir(source.CacheRoot, 0700); err != nil {
		t.Fatal(err)
	}
	head := RepositoryRevision{Number: 1, UUID: "test-incarnation"}
	first, err := source.history(t.Context(), "repo", url, head)
	if err != nil || len(first.Entries) != 1 || first.Entries[0].FirstCommitter != "before" {
		t.Fatal(first, err)
	}
	cachePath := filepath.Join(source.CacheRoot, "repo.history.json")
	before, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := source.history(t.Context(), "repo", url, head); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal(got, err)
	}
	after, err := os.Stat(cachePath)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("unchanged cache rewritten", err)
	}
	value := filepath.Join(root, "author")
	for _, author := range []string{"after", ""} {
		if err := os.WriteFile(value, []byte(author), 0600); err != nil {
			t.Fatal(err)
		}
		run(admin, "setrevprop", repo, "-r", "1", "svn:author", value)
		got, err := source.history(t.Context(), "repo", url, head)
		if err != nil || got.Entries[0].FirstCommitter != author || got.Entries[0].ID != first.Entries[0].ID || got.Revision != 1 {
			t.Fatal(got, err)
		}
	}
	// A failed source read must not serve previously cached ownership as fresh.
	source.SVN = filepath.Join(root, "absent")
	if _, err := source.history(t.Context(), "repo", url, head); err == nil {
		t.Fatal("failed source accepted cache")
	}
}
