//go:build native_svn_probe && (windows || linux)

package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/svnurl"
)

// A repository just created has only r0. Its baseline is r0, not an error:
// before this, the first commit to every new repository failed when
// working-copy operations went through the helper.
func TestNativeCommitHeadOfAnEmptyRepositoryIsRevisionZero(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command(nativeFixtureTool(t, "svnadmin"), "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(t.TempDir(), "absent-cli")}).(*execClient)
	head, err := c.CommitHead(context.Background(), svnurl.File(repo))
	if err != nil || head != 0 {
		t.Fatalf("empty repository baseline = %d, %v", head, err)
	}
}
