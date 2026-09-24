//go:build !windows

package servertool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A lock on a file with a Polish name must reach the pre-lock hook. svnserve
// converts hook arguments to the native encoding; the empty environment it
// used to get made that ASCII, and "Failed to start ... pre-lock hook" was the
// answer to every such lock (demo.filees.space, 2026-09-24). The control run
// with an empty environment proves the test still sees the defect.
func TestSVNServeEnvironmentLetsHooksReceiveUTF8Paths(t *testing.T) {
	var tools [3]string
	for i, name := range []string{"svnadmin", "svnserve", "svn"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s not installed", name)
		}
		tools[i] = path
	}
	svnadmin, svnserve, svn := tools[0], tools[1], tools[2]
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	utf8 := append(os.Environ(), "LC_ALL=", "LC_CTYPE=C.UTF-8")
	run := func(dir string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = dir, utf8
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run(root, svnadmin, "create", repo)
	if err := os.WriteFile(filepath.Join(repo, "hooks", "pre-lock"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(root, "wc")
	run(root, svn, "checkout", "-q", "file://"+repo, wc)
	const name = "ujęcie 008.jpg"
	if err := os.WriteFile(filepath.Join(wc, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(wc, svn, "add", "-q", name)
	run(wc, svn, "commit", "-q", "-m", "init")

	lock := func(environment []string) (string, error) {
		tunnel := filepath.Join(root, "tunnel.sh")
		script := "#!/bin/sh\nexec env -i " + strings.Join(environment, " ") + " " + svnserve + " -t -r " + root + "\n"
		if err := os.WriteFile(tunnel, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(svn, "lock", "-m", "probe", "--non-interactive",
			"--config-option", "config:tunnels:probe="+tunnel, "svn+probe://host/repo/"+name)
		cmd.Env = utf8
		out, err := cmd.CombinedOutput()
		unlock := exec.Command(svnadmin, "rmlocks", repo, name)
		unlock.Env = utf8
		_ = unlock.Run()
		return string(out), err
	}
	if out, err := lock(nil); err == nil || !strings.Contains(out, "pre-lock") {
		t.Fatalf("control: an empty environment no longer breaks the hook (err=%v)\n%s", err, out)
	}
	if out, err := lock(svnserveEnvironment); err != nil {
		t.Fatalf("lock with svnserveEnvironment: %v\n%s", err, out)
	}
}
