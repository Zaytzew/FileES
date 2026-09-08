package repoworker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"github.com/google/uuid"
)

func TestDeletionSafetyUnderOpenBSDSandbox(t *testing.T) {
	if os.Getenv("FILEES_DELETION_SANDBOX_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestDeletionSafetyUnderOpenBSDSandbox$", "-test.v")
		cmd.Env = append(os.Environ(), "FILEES_DELETION_SANDBOX_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sandbox child: %v\n%s", err, out)
		}
		return
	}
	e, repoID, _ := deletionFixture(t, 7, time.Now())
	if err := os.MkdirAll(e.DeletionArchiveRoot, 0700); err != nil {
		t.Fatal(err)
	}
	// Match the repository worker's proc/exec capabilities. Keep filesystem
	// grants confined to the fixture plus the SVN runtime dependencies.
	const promises = "stdio rpath wpath cpath fattr flock proc exec"
	// freeze itself execs dump. The ClientControlCommand ceiling includes
	// exec; the narrower svnExecPromises used by other entrypoints does not.
	const childPromises = "stdio rpath wpath cpath fattr flock proc exec prot_exec unveil"
	if err := obsandbox.Begin(promises); err != nil {
		t.Fatal(err)
	}
	paths := []obsandbox.Path{
		{Label: "repos", Name: filepath.Dir(e.RepositoriesRoot), Perms: "rwc"},
		{Label: "archives", Name: filepath.Dir(e.DeletionArchiveRoot), Perms: "rwc"},
		{Label: "svnadmin", Name: e.SVNAdmin, Perms: "rx"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "base-libs", Name: "/usr/lib", Perms: "r"},
		{Label: "svn-libs", Name: "/usr/local/lib", Perms: "r"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "null", Name: "/dev/null", Perms: "rw"},
	}
	if err := obsandbox.ApplyForExec(obsandbox.Profile{Name: "deletion-safety-test", Promises: promises, Paths: paths}, childPromises); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ArchiveAndDeleteFSFS(context.Background(), repoID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
}
