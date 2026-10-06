package mobileclient

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

func TestDirectoryCachePrunesOnlyDominatedPairs(t *testing.T) {
	s := Store{Root: t.TempDir()}
	save := func(repo, path string, gen, rev int64) {
		t.Helper()
		if err := s.SaveDirectory(path, &v1.Manifest{Schema: v1.ManifestSchema, RepoID: repo, ViewGeneration: gen, RepoRevision: rev}); err != nil {
			t.Fatal(err)
		}
	}
	save("a", "", 1, 5)
	save("other", "", 1, 5)
	unknown := filepath.Join(s.Root, "dir-listings", "a", "1", "5", "keep.txt")
	if err := os.WriteFile(unknown, []byte("unrecognized"), 0600); err != nil {
		t.Fatal(err)
	}
	save("a", "", 2, 6)
	save("a", "docs", 2, 6)
	save("a", "late", 1, 5)
	if got, err := s.LoadDirectory("a", "", 1, 5); err != nil || got != nil {
		t.Fatalf("old root retained %v %v", got, err)
	}
	if got, err := s.LoadNewestDirectory("a", "late"); err != nil || got == nil || got.RepoRevision != 5 {
		t.Fatalf("unrefreshed folder lost its offline page: %+v %v", got, err)
	}
	for _, path := range []string{"", "docs"} {
		if got, err := s.LoadDirectory("a", path, 2, 6); err != nil || got == nil {
			t.Fatalf("new page lost %v", err)
		}
	}
	if got, _ := s.LoadDirectory("other", "", 1, 5); got == nil {
		t.Fatal("other repo touched")
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unknown file touched", err)
	}
	save("a", "", 3, 5) // Not comparable: do not discard a higher revision.
	if got, _ := s.LoadDirectory("a", "", 2, 6); got == nil {
		t.Fatal("higher revision lost")
	}
	save("a", "", 3, 7)
	if got, _ := s.LoadDirectory("a", "", 3, 5); got != nil {
		t.Fatal("dominated pair remains")
	}
	if err := s.SaveDirectory("", &v1.Manifest{Schema: v1.ManifestSchema, RepoID: "../outside", ViewGeneration: 1, RepoRevision: 1}); err == nil {
		t.Fatal("unsafe repo accepted")
	}
}

func TestDirectoryCacheKeepsLastPagePerPath(t *testing.T) {
	s := Store{Root: t.TempDir()}
	save := func(path string, rev int64) {
		t.Helper()
		if err := s.SaveDirectory(path, &v1.Manifest{Schema: v1.ManifestSchema, RepoID: "repo-1", ViewGeneration: 1, RepoRevision: rev}); err != nil {
			t.Fatal(err)
		}
	}
	save("", 4)
	save("docs", 4)
	save("", 5) // A new root page must not erase the last known docs page.
	page, err := s.LoadNewestDirectory("repo-1", "docs")
	if err != nil || page == nil || page.RepoRevision != 4 {
		t.Fatalf("offline docs page: %+v %v", page, err)
	}
	save("docs", 5)
	if old, err := s.LoadDirectory("repo-1", "docs", 1, 4); err != nil || old != nil {
		t.Fatalf("replaced docs page retained: %+v %v", old, err)
	}
}

func TestDirectoryCacheConcurrentOldRepliesKeepNewest(t *testing.T) {
	s := Store{Root: t.TempDir()}
	var wg sync.WaitGroup
	for rev := int64(1); rev <= 12; rev++ {
		wg.Add(1)
		go func(rev int64) {
			defer wg.Done()
			if err := s.SaveDirectory("", &v1.Manifest{Schema: v1.ManifestSchema, RepoID: "r", ViewGeneration: 1, RepoRevision: rev}); err != nil {
				t.Error(err)
			}
		}(rev)
	}
	wg.Wait()
	pairs := s.listingPairs("r")
	if len(pairs) != 1 || pairs[0].rev != 12 {
		t.Fatalf("pairs: %+v", pairs)
	}
}
