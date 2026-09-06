//go:build openbsd

package servertool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"filees/internal/obsandbox"
	"filees/pkg/repoworker"
)

func TestLockGuardsUnderSVNChildPromises(t *testing.T) {
	if raw := os.Getenv("FILEES_TEST_HOOK_ARGV"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
		if root := os.Getenv("FILEES_TEST_HOOK_UNVEIL"); root != "" {
			profile := obsandbox.Profile{Name: "lock-guard-control-unveil", Promises: "stdio proc exec", Paths: []obsandbox.Path{
				{Label: "fixture", Name: root, Perms: "rwc"},
				{Label: "fixture-parent", Name: filepath.Dir(root), Perms: "r"},
				{Label: "svn", Name: args[0], Perms: "rx"},
				{Label: "guard-worker", Name: os.Args[0], Perms: "rx"},
				{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
				{Label: "hints", Name: "/var/run/ld.so.hints", Perms: "r"},
				{Label: "system-libs", Name: "/usr/lib", Perms: "r"},
				{Label: "local-libs", Name: "/usr/local/lib", Perms: "r"},
				{Label: "config", Name: "/etc/subversion", Perms: "r"},
				{Label: "null", Name: "/dev/null", Perms: "rw"},
				{Label: "random", Name: "/dev/urandom", Perms: "r"},
			}}
			if err := sandboxApplyForExec(profile, svnHookExecPromises); err != nil {
				t.Fatal(err)
			}
		} else if err := sandboxPledgeForExec("stdio proc exec", svnHookExecPromises); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Exec(args[0], args, []string{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	tools := requireSVN(t, "svn", "svnadmin")
	svn, admin := tools[0], tools[1]
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	runSupervisorCommand(t, admin, "create", repo)
	runSupervisorCommand(t, svn, "co", "file://"+repo, wc)
	doc := filepath.Join(wc, "file")
	if err := os.WriteFile(doc, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	runSupervisorCommand(t, svn, "add", doc)
	runSupervisorCommand(t, svn, "ci", "-m", "fixture", wc)
	if err := repoworker.InstallLockGuards(repo, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	for _, lockedUnveil := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			args := []string{svn, "lock", "--username", "holder", "-m", "ordinary"}
			if force {
				args = append(args, "--force")
			}
			args = append(args, doc)
			raw, _ := json.Marshal(args)
			child := exec.Command(os.Args[0], "-test.run=^TestLockGuardsUnderSVNChildPromises$")
			child.Dir = root // APR chdir(".") must stay inside the unveiled tree
			child.Env = append(os.Environ(), "FILEES_TEST_HOOK_ARGV="+string(raw))
			if lockedUnveil {
				child.Env = append(child.Env, "FILEES_TEST_HOOK_UNVEIL="+root)
			}
			out, err := child.CombinedOutput()
			if !force && err != nil {
				t.Fatalf("normal lock under child promises: %v %s", err, out)
			}
			if force && (err == nil || !strings.Contains(string(out), "forced lock replacement/release is forbidden")) {
				t.Fatalf("force guard not reached: %v %s", err, out)
			}
		}
		runSupervisorCommand(t, svn, "unlock", "--username", "holder", doc)
	}
}

func TestLockGuardSVNTunnelChild(t *testing.T) {
	root, binary := os.Getenv("FILEES_TEST_TUNNEL_ROOT"), os.Getenv("FILEES_TEST_TUNNEL_SVNSERVE")
	if root == "" || binary == "" {
		return
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if err := sandboxPledgeForExec("stdio proc exec", svnHookExecPromises); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Exec(binary, []string{binary, "-t", "--tunnel-user", "holder", "-r", root}, []string{}); err != nil {
		t.Fatal(err)
	}
}

func TestLockGuardsThroughNativeSVNServe(t *testing.T) {
	tools := requireSVN(t, "svn", "svnadmin", "svnserve")
	svn, admin, serve := tools[0], tools[1], tools[2]
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	runSupervisorCommand(t, admin, "create", repo)
	runSupervisorCommand(t, svn, "co", "file://"+repo, wc)
	doc := filepath.Join(wc, "file")
	if err := os.WriteFile(doc, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	runSupervisorCommand(t, svn, "add", doc)
	runSupervisorCommand(t, svn, "ci", "-m", "fixture", wc)
	if err := repoworker.InstallLockGuards(repo, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	url := "svn+ssh://guard-test/repo/file"
	for _, action := range []struct {
		verb  string
		force bool
	}{{"lock", false}, {"lock", true}, {"unlock", true}, {"unlock", false}} {
		args := []string{"--non-interactive", "--no-auth-cache", "--config-dir", filepath.Join(root, "config"), action.verb}
		if action.force {
			args = append(args, "--force")
		}
		args = append(args, url)
		command := exec.Command(svn, args...)
		command.Dir = root
		command.Env = append(os.Environ(), "SVN_SSH="+os.Args[0]+" -test.run=^TestLockGuardSVNTunnelChild$ --", "FILEES_TEST_TUNNEL_ROOT="+root, "FILEES_TEST_TUNNEL_SVNSERVE="+serve)
		out, err := command.CombinedOutput()
		if !action.force && err != nil {
			t.Fatalf("ordinary %s: %v %s", action.verb, err, out)
		}
		if action.force && (err == nil || !strings.Contains(string(out), "forced lock replacement/release is forbidden")) {
			t.Fatalf("force %s: %v %s", action.verb, err, out)
		}
	}
}
