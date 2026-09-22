package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/svnurl"
)

type sparseDepthClient interface {
	CheckoutDepthEmpty(context.Context, string, string) (string, error)
	UpdateSetDepth(context.Context, string, string, string) (string, error)
	Update(context.Context, string) (string, error)
}

// sparseDepthScenario is the unattached browser's working copy on real svn:
// an empty root, one deep path chosen alone with its parents, a plain update
// that keeps exactly that choice, then the whole tree in the same copy.
func sparseDepthScenario(t *testing.T, c sparseDepthClient, run func(string, ...string)) {
	t.Helper()
	root := t.TempDir()
	repo, seed, wc := filepath.Join(root, "repo"), filepath.Join(root, "seed"), filepath.Join(root, "wc")
	run("svnadmin", "create", repo)
	run("svn", "checkout", svnurl.File(repo), seed)
	for _, file := range []string{"art/chosen/model.blend", "art/other/skip.psd", "docs/readme.txt"} {
		path := filepath.Join(seed, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("svn", "add", "--force", seed)
	run("svn", "commit", seed, "-m", "tree")

	ctx := context.Background()
	if _, err := c.CheckoutDepthEmpty(ctx, svnurl.File(repo), wc); err != nil {
		t.Fatal(err)
	}
	// A FileES working copy carries .filees; the native helper deepens only
	// such a live copy, as the daemon's attachment writes it before choosing.
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(wc, filepath.FromSlash(rel)))
		return err == nil
	}
	if _, err := c.UpdateSetDepth(ctx, wc, "art/chosen", "infinity"); err != nil {
		t.Fatalf("choose a deep path: %v", err)
	}
	if !exists("art/chosen/model.blend") || exists("art/other") || exists("docs") {
		t.Fatal("the chosen path did not arrive alone")
	}
	if _, err := c.Update(ctx, wc); err != nil {
		t.Fatal(err)
	}
	if exists("art/other") || exists("docs") {
		t.Fatal("a plain update widened the user's choice")
	}
	if _, err := c.UpdateSetDepth(ctx, wc, ".", "infinity"); err != nil {
		t.Fatalf("whole tree: %v", err)
	}
	if !exists("art/other/skip.psd") || !exists("docs/readme.txt") || !exists("art/chosen/model.blend") {
		t.Fatal("the whole tree did not arrive in the same copy")
	}
}

func TestUpdateSetDepthChoosesPathsInASparseCopy(t *testing.T) {
	for _, tool := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	run := func(tool string, args ...string) {
		t.Helper()
		if out, err := exec.Command(tool, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v %s", tool, args, err, out)
		}
	}
	c := New(Options{SvnPath: "svn"}).(*execClient)
	sparseDepthScenario(t, c, run)
}
