package authority

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestSVNLookEnvironmentForcesUTF8(t *testing.T) {
	got := svnLookEnvironment([]string{"PATH=/bin", "LC_ALL=C", "LANG=pl_PL.UTF-8"})
	wantLocale := 0
	for _, entry := range got {
		if entry == "LC_ALL=C.UTF-8" {
			wantLocale++
		}
		if entry == "LC_ALL=C" {
			t.Fatal("legacy LC_ALL survived in svnlook environment")
		}
	}
	if wantLocale != 1 {
		t.Fatalf("LC_ALL=C.UTF-8 count = %d, want 1", wantLocale)
	}
}

func TestSVNLookTreeEnumeratesExactSourceRoot(t *testing.T) {
	svnadmin, errAdmin := exec.LookPath("svnadmin")
	svn, errSVN := exec.LookPath("svn")
	svnlook, errLook := exec.LookPath("svnlook")
	if errAdmin != nil || errSVN != nil || errLook != nil {
		t.Skip("Subversion tools unavailable")
	}
	root := t.TempDir()
	repoID := uuid.NewString()
	repository := filepath.Join(root, repoID)
	if out, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	input := t.TempDir()
	if err := os.MkdirAll(filepath.Join(input, "folder", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"root.txt": "root", "folder/a.txt": "a", "folder/nested/b.txt": "b"} {
		if err := os.WriteFile(filepath.Join(input, filepath.FromSlash(name)), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command(svn, "import", input, "file://"+repository, "-m", "fixture").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v: %s", err, out)
	}
	source := SVNLookSource{SVNLook: svnlook, RepositoriesRoot: root}
	objects, err := source.Tree(context.Background(), repoID, "folder", 1)
	if err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	want := []TreeObject{{RepoPath: "folder/a.txt", DisplayName: "a.txt", Size: &one}, {RepoPath: "folder/nested/b.txt", DisplayName: "nested/b.txt", Size: &one}}
	if !reflect.DeepEqual(objects, want) {
		t.Fatalf("tree = %#v, want %#v", objects, want)
	}
}
