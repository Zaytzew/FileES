package mobileworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

type fakeAuthority struct {
	repoPath string
	gen      int64
	access   string
}

func (f fakeAuthority) Resolve(context.Context, string, string) (View, error) {
	return View{RepoPath: f.repoPath, Generation: f.gen, Access: f.access}, nil
}

func requireSVN(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"svn", "svnadmin", "svnlook"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, errb.String())
	}
}

// newSeededRepo creates a repository with a fixed tree at r1 and returns its path.
func newSeededRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	run(t, "svnadmin", "create", repo)

	seed := filepath.Join(dir, "seed")
	writeSeed(t, seed, "photos/2026/a.jpg", []byte("hello"))
	writeSeed(t, seed, "docs/b.bin", []byte{0, 1, 2, 3})
	writeSeed(t, seed, "top.txt", []byte("top"))
	run(t, "svn", "import", "-q", seed, fileURL(repo), "-m", "seed import")
	return repo
}

func writeSeed(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// commitFileInto adds a new file into an existing directory and commits it.
func commitFileInto(t *testing.T, repo, rel string, data []byte) {
	t.Helper()
	wc := t.TempDir()
	run(t, "svn", "checkout", "-q", fileURL(repo), wc)
	full := filepath.Join(wc, filepath.FromSlash(rel))
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, "svn", "add", "-q", full)
	run(t, "svn", "commit", "-q", wc, "-m", "append file")
}

func newBrowser(repo string, gen int64, access string) Browser {
	return Browser{Authority: fakeAuthority{repoPath: repo, gen: gen, access: access}, Reader: SVNReader{}}
}

func TestRefreshManifestBuildsTree(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	b := newBrowser(repo, 5, "r")

	res, err := b.RefreshManifest(context.Background(), "client-1", v1.RefreshManifestPayload{RepoID: "repo-1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.NotModified || res.Manifest == nil {
		t.Fatalf("expected a manifest, got %+v", res)
	}
	m := res.Manifest
	if m.ViewGeneration != 5 || m.RepoRevision != 1 {
		t.Fatalf("view_generation/repo_revision = %d/%d", m.ViewGeneration, m.RepoRevision)
	}
	want := map[string]v1.Kind{
		"docs": v1.KindDirectory, "docs/b.bin": v1.KindFile,
		"photos": v1.KindDirectory, "photos/2026": v1.KindDirectory,
		"photos/2026/a.jpg": v1.KindFile, "top.txt": v1.KindFile,
	}
	got := map[string]v1.Kind{}
	for _, e := range m.Entries {
		got[e.Path] = e.Kind
		if e.ContentHash != nil {
			t.Fatalf("content_hash should be nil in a listing, path %q", e.Path)
		}
	}
	for path, kind := range want {
		if got[path] != kind {
			t.Fatalf("entry %q = %q, want %q", path, got[path], kind)
		}
	}
	for _, e := range m.Entries {
		if e.Path == "photos/2026/a.jpg" && e.Size != 5 {
			t.Fatalf("a.jpg size = %d, want 5", e.Size)
		}
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("built manifest invalid: %v", err)
	}
}

func TestRefreshNotModifiedNeedsBothDimensions(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	b := newBrowser(repo, 5, "r")

	// Both known values current -> NOT_MODIFIED.
	res, err := b.RefreshManifest(context.Background(), "c", v1.RefreshManifestPayload{RepoID: "r", KnownViewGeneration: 5, KnownRepoRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NotModified {
		t.Fatal("expected NOT_MODIFIED when both dimensions match")
	}

	// Stale generation alone (repo unchanged) -> manifest. This is the shout case.
	res, err = b.RefreshManifest(context.Background(), "c", v1.RefreshManifestPayload{RepoID: "r", KnownViewGeneration: 4, KnownRepoRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.NotModified || res.Manifest == nil {
		t.Fatal("stale view_generation must return a manifest even if the revision is current")
	}
}

func TestRefreshRepoBumpReturnsManifest(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	b := newBrowser(repo, 5, "r")

	commitFileInto(t, repo, "photos/2026/c.jpg", []byte("world!"))

	res, err := b.RefreshManifest(context.Background(), "c", v1.RefreshManifestPayload{RepoID: "r", KnownViewGeneration: 5, KnownRepoRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.NotModified || res.Manifest == nil {
		t.Fatal("stale repo_revision must return a manifest")
	}
	if res.Manifest.RepoRevision != 2 {
		t.Fatalf("repo_revision = %d, want 2", res.Manifest.RepoRevision)
	}
	found := false
	for _, e := range res.Manifest.Entries {
		if e.Path == "photos/2026/c.jpg" {
			found = true
		}
	}
	if !found {
		t.Fatal("new file missing from manifest")
	}
}

func TestReadObjectStreamsContent(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	b := newBrowser(repo, 5, "r")

	var buf bytes.Buffer
	res, err := b.ReadObject(context.Background(), "c", v1.ReadObjectPayload{RepoID: "r", Path: "photos/2026/a.jpg"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello" {
		t.Fatalf("content = %q, want hello", buf.String())
	}
	if res.Size != 5 {
		t.Fatalf("size = %d, want 5", res.Size)
	}
	sum := sha256.Sum256([]byte("hello"))
	if res.Sha256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %s", res.Sha256)
	}
}

func TestReadObjectRejectsDirectoryAndAbsent(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	b := newBrowser(repo, 5, "r")

	if _, err := b.ReadObject(context.Background(), "c", v1.ReadObjectPayload{RepoID: "r", Path: "photos"}, &bytes.Buffer{}); err == nil {
		t.Fatal("reading a directory as an object should fail")
	}
	if _, err := b.ReadObject(context.Background(), "c", v1.ReadObjectPayload{RepoID: "r", Path: "nope.txt"}, &bytes.Buffer{}); err == nil {
		t.Fatal("reading an absent path should fail")
	}
}

func TestAccessDenied(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	b := newBrowser(repo, 5, "") // no grant

	if _, err := b.RefreshManifest(context.Background(), "c", v1.RefreshManifestPayload{RepoID: "r"}); err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
	if _, err := b.ReadObject(context.Background(), "c", v1.ReadObjectPayload{RepoID: "r", Path: "top.txt"}, &bytes.Buffer{}); err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}
