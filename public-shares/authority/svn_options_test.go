package authority

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/svnurl"
	"github.com/google/uuid"
)

func TestSVNLookTreatsLeadingHyphensAsPaths(t *testing.T) {
	for _, tool := range []string{"svnadmin", "svn", "svnlook"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable", tool)
		}
	}
	root, input := t.TempDir(), t.TempDir()
	id := uuid.NewString()
	repo := filepath.Join(root, id)
	if out, err := exec.Command("svnadmin", "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("create: %v: %s", err, out)
	}
	if err := os.Mkdir(filepath.Join(input, "--help"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"--version", "--help/data.txt"} {
		if err := os.WriteFile(filepath.Join(input, filepath.FromSlash(name)), []byte("actual content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("svn", "import", "-m", "fixture", "--", input, svnurl.File(repo)).CombinedOutput(); err != nil {
		t.Fatalf("import: %v: %s", err, out)
	}
	svnlook, _ := exec.LookPath("svnlook")
	source := SVNLookSource{SVNLook: svnlook, RepositoriesRoot: root}
	ctx := context.Background()
	var content bytes.Buffer
	if err := source.Cat(ctx, id, "--version", 1, &content); err != nil || content.String() != "actual content" {
		t.Errorf("cat: %q, %v", content.String(), err)
	}
	for _, path := range []string{".", "--help"} {
		objects, err := source.Tree(ctx, id, path, 1)
		if err != nil || len(objects) == 0 {
			t.Fatalf("tree %q: %v, %v", path, objects, err)
		}
		for _, object := range objects {
			if object.Size == nil || *object.Size != int64(len("actual content")) {
				t.Errorf("filesize %q: %v", object.RepoPath, object.Size)
			}
		}
	}
	if head, err := source.Head(ctx, id); err != nil || head != 1 {
		t.Errorf("head: %d, %v", head, err)
	}
	if date, err := source.RevisionDate(ctx, id, 1); err != nil || date.IsZero() {
		t.Errorf("date: %v, %v", date, err)
	}
}
