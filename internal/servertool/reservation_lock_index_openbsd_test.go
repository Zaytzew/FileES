//go:build openbsd

package servertool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filees/internal/obsandbox"
	"filees/internal/svnurl"
)

func TestLockIndexUnderReadOnlyServingSandbox(t *testing.T) {
	if root := os.Getenv("FILEES_LOCK_INDEX_SANDBOX_ROOT"); root != "" {
		admin := os.Getenv("FILEES_LOCK_INDEX_SANDBOX_ADMIN")
		profile := obsandbox.Profile{Name: "serving-lock-index-test", Promises: reservationWorkerRuntimePromises, Paths: []obsandbox.Path{
			{Label: "repository", Name: root, Perms: "r"},
			{Label: "repository-parent", Name: filepath.Dir(root), Perms: "r"},
			{Label: "svnadmin", Name: admin, Perms: "rx"},
			{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
			{Label: "hints", Name: "/var/run/ld.so.hints", Perms: "r"},
			{Label: "system-libs", Name: "/usr/lib", Perms: "r"},
			{Label: "local-libs", Name: "/usr/local/lib", Perms: "r"},
			{Label: "locale", Name: "/usr/share/locale", Perms: "r"},
			{Label: "null", Name: "/dev/null", Perms: "rw"},
			{Label: "random", Name: "/dev/urandom", Perms: "r"},
		}}
		if err := sandboxApplyForExec(profile, svnExecPromises); err != nil {
			t.Fatal(err)
		}
		rows, err := queryLockIndex(context.Background(), admin, root)
		if err != nil || len(rows) != 1 || rows[0].Path != "żółć.txt" || rows[0].Comment != "first\nPath: /not-a-record\nlast" {
			t.Fatalf("sandbox index: %v %v", rows, err)
		}
		return
	}
	tools := requireSVN(t, "svn", "svnadmin")
	svn, admin := tools[0], tools[1]
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	run := func(bin string, args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=", "LC_CTYPE=C.UTF-8")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	run(admin, "create", repo)
	run(svn, "co", svnurl.File(repo), wc)
	file := filepath.Join(wc, "żółć.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	run(svn, "add", file)
	run(svn, "ci", "-m", "fixture", wc)
	run(svn, "lock", "-m", "first\nPath: /not-a-record\nlast", file)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockIndexUnderReadOnlyServingSandbox$")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "FILEES_LOCK_INDEX_SANDBOX_ROOT="+repo, "FILEES_LOCK_INDEX_SANDBOX_ADMIN="+admin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox subprocess: %v %s", err, out)
	}
}
