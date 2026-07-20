package mobileclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/mobileworker"
	v1 "filees/pkg/mobile/v1"

	"github.com/google/uuid"
)

// --- fixture (real ephemeral SVN + in-process dispatcher) ---

type fakeAuth struct {
	repoPath string
	gen      int64
	access   string
}

func (f fakeAuth) Resolve(context.Context, string, string) (mobileworker.View, error) {
	return mobileworker.View{RepoPath: f.repoPath, Generation: f.gen, Access: f.access}, nil
}

// dispatcherTransport runs each operation through a real dispatcher as one
// framed session — the same path a real SSH session would drive.
type dispatcherTransport struct{ d mobileworker.Dispatcher }

func (t dispatcherTransport) Do(ctx context.Context, req v1.Request, reqPayload []byte) (v1.Response, []byte, error) {
	header, err := json.Marshal(req)
	if err != nil {
		return v1.Response{}, nil, err
	}
	var in bytes.Buffer
	if err := v1.WriteFrame(&in, v1.RequestMagic, header, reqPayload); err != nil {
		return v1.Response{}, nil, err
	}
	var out bytes.Buffer
	if err := t.d.Serve(ctx, &in, &out); err != nil {
		return v1.Response{}, nil, err
	}
	h, p, err := v1.ReadFrame(&out, v1.ResponseMagic, v1.MaxHeaderBytes)
	if err != nil {
		return v1.Response{}, nil, err
	}
	resp, err := v1.ParseResponse(h)
	return resp, p, err
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

func fileURL(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

func newSeededRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	run(t, "svnadmin", "create", repo)
	seed := filepath.Join(dir, "seed")
	if err := os.MkdirAll(filepath.Join(seed, "photos", "2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "photos", "2026", "a.jpg"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, "svn", "import", "-q", seed, fileURL(repo), "-m", "seed import")
	return repo
}

func commitFileInto(t *testing.T, repo, rel string, data []byte) {
	t.Helper()
	wc := t.TempDir()
	run(t, "svn", "checkout", "-q", fileURL(repo), wc)
	if err := os.WriteFile(filepath.Join(wc, filepath.FromSlash(rel)), data, 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, "svn", "add", "-q", filepath.Join(wc, filepath.FromSlash(rel)))
	run(t, "svn", "commit", "-q", wc, "-m", "append file")
}

func newClient(t *testing.T, repo, access string) Client {
	t.Helper()
	auth := fakeAuth{repoPath: repo, gen: 7, access: access}
	d := mobileworker.Dispatcher{
		Browser:  mobileworker.Browser{Authority: auth, Reader: mobileworker.SVNReader{}},
		Appender: mobileworker.Appender{Authority: auth, Reader: mobileworker.SVNReader{}, Committer: mobileworker.SVNAppender{}, Ledger: mobileworker.Ledger{Dir: t.TempDir()}},
		ClientID: "client-1",
	}
	return Client{Transport: dispatcherTransport{d: d}, Store: Store{Root: t.TempDir()}}
}

// --- tests ---

func TestRefreshFetchesAndCaches(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "r")

	m, err := c.Refresh(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.ViewGeneration != 7 || m.RepoRevision != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	cached, err := c.Store.LoadManifest("repo-1")
	if err != nil || cached == nil {
		t.Fatalf("manifest not cached: %v", err)
	}
}

func TestRefreshNotModifiedKeepsCache(t *testing.T) {
	requireSVN(t)
	c := newClient(t, newSeededRepo(t), "r")

	if _, err := c.Refresh(context.Background(), "repo-1"); err != nil {
		t.Fatal(err)
	}
	// A direct request with the cached known state must return NOT_MODIFIED.
	req, _ := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo-1", KnownViewGeneration: 7, KnownRepoRevision: 1})
	resp, _, err := c.Transport.Do(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	var res v1.RefreshManifestResult
	json.Unmarshal(resp.Result, &res)
	if !res.NotModified {
		t.Fatalf("expected NOT_MODIFIED, got %+v", res)
	}

	m, err := c.Refresh(context.Background(), "repo-1")
	if err != nil || m == nil || m.RepoRevision != 1 {
		t.Fatalf("refresh after NOT_MODIFIED = %+v err=%v", m, err)
	}
}

func TestRefreshRepoBumpUpdatesCache(t *testing.T) {
	requireSVN(t)
	repo := newSeededRepo(t)
	c := newClient(t, repo, "r")

	if _, err := c.Refresh(context.Background(), "repo-1"); err != nil {
		t.Fatal(err)
	}
	commitFileInto(t, repo, "photos/2026/c.jpg", []byte("world!"))

	m, err := c.Refresh(context.Background(), "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.RepoRevision != 2 {
		t.Fatalf("repo_revision = %d, want 2", m.RepoRevision)
	}
	found := false
	for _, e := range m.Entries {
		if e.Path == "photos/2026/c.jpg" {
			found = true
		}
	}
	if !found {
		t.Fatal("new file missing from refreshed manifest")
	}
	cached, _ := c.Store.LoadManifest("repo-1")
	if cached == nil || cached.RepoRevision != 2 {
		t.Fatalf("cache not updated: %+v", cached)
	}
}

func TestSaveManifestRejectsRollback(t *testing.T) {
	s := Store{Root: t.TempDir()}
	base := &v1.Manifest{Schema: v1.ManifestSchema, RepoID: "r", ViewGeneration: 2, RepoRevision: 5, Complete: true}
	if ok, err := s.SaveManifestIfNewer(base); err != nil || !ok {
		t.Fatalf("initial save: ok=%v err=%v", ok, err)
	}
	cases := []struct {
		gen, rev int64
		wantOK   bool
		wantErr  bool
	}{
		{1, 5, false, true},  // older generation
		{2, 4, false, true},  // older revision
		{2, 5, false, false}, // equal -> no-op
		{3, 5, true, false},  // newer generation
		{3, 6, true, false},  // newer both
	}
	for _, tc := range cases {
		m := &v1.Manifest{Schema: v1.ManifestSchema, RepoID: "r", ViewGeneration: tc.gen, RepoRevision: tc.rev, Complete: true}
		ok, err := s.SaveManifestIfNewer(m)
		if tc.wantErr && err == nil {
			t.Fatalf("gen=%d rev=%d: expected rollback error", tc.gen, tc.rev)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("gen=%d rev=%d: unexpected error %v", tc.gen, tc.rev, err)
		}
		if ok != tc.wantOK {
			t.Fatalf("gen=%d rev=%d: ok=%v want %v", tc.gen, tc.rev, ok, tc.wantOK)
		}
	}
}
