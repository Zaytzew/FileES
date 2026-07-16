//go:build openbsd

package obsandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenBSDUnveilAllowsOnlyProfilePath(t *testing.T) {
	if os.Getenv("FILEES_SANDBOX_PROBE") == "unveil" {
		root := os.Getenv("FILEES_SANDBOX_ROOT")
		if err := Begin("stdio rpath"); err != nil {
			t.Fatal(err)
		}
		if err := Apply(Profile{Name: "probe/unveil", Promises: "stdio rpath", Paths: []Path{{Label: "allowed", Name: root, Perms: "r"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.ReadFile(filepath.Join(root, "allowed")); err != nil {
			t.Fatalf("allowed read: %v", err)
		}
		if _, err := os.ReadFile("/etc/master.passwd"); err == nil {
			t.Fatal("foreign read succeeded")
		}
		return
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "allowed"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestOpenBSDUnveilAllowsOnlyProfilePath$")
	command.Env = append(os.Environ(), "FILEES_SANDBOX_PROBE=unveil", "FILEES_SANDBOX_ROOT="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("unveil probe: %v: %s", err, output)
	}
}

func TestOpenBSDPledgeKillsNetworkOutsideProfile(t *testing.T) {
	if os.Getenv("FILEES_SANDBOX_PROBE") == "network" {
		root := os.Getenv("FILEES_SANDBOX_ROOT")
		if err := Begin("stdio rpath"); err != nil {
			t.Fatal(err)
		}
		if err := Apply(Profile{Name: "probe/no-network", Promises: "stdio rpath", Paths: []Path{{Label: "allowed", Name: root, Perms: "r"}}}); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			listener.Close()
			t.Fatal("network listener succeeded")
		}
		return
	}
	expectPledgeDeath(t, "network", t.TempDir())
}

func TestOpenBSDPledgeKillsExecWithoutExecPromise(t *testing.T) {
	if os.Getenv("FILEES_SANDBOX_PROBE") == "exec" {
		if err := unix.PledgePromises("stdio rpath"); err != nil {
			t.Fatal(err)
		}
		_ = exec.Command("/bin/echo", "forbidden").Run()
		t.Fatal("exec returned without pledge termination")
	}
	expectPledgeDeath(t, "exec", "")
}

func TestOpenBSDForcedDispatcherMayExecOnlyUnveiledWorker(t *testing.T) {
	if os.Getenv("FILEES_SANDBOX_PROBE") == "allowed-exec-child" {
		fmt.Println("filees-exec-ok")
		return
	}
	if os.Getenv("FILEES_SANDBOX_PROBE") == "allowed-exec" {
		if err := Begin("stdio proc exec"); err != nil {
			t.Fatal(err)
		}
		profile := Profile{Name: "probe/allowed-exec", Promises: "stdio proc exec", Paths: []Path{
			{Label: "command", Name: os.Args[0], Perms: "rx"},
			{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
			{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		}}
		if err := ApplyForExec(profile, "stdio rpath wpath cpath fattr flock inet proc unveil"); err != nil {
			t.Fatal(err)
		}
		_ = syscall.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestOpenBSDForcedDispatcherMayExecOnlyUnveiledWorker$"}, []string{"FILEES_SANDBOX_PROBE=allowed-exec-child"})
		t.Fatal("allowed exec returned")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestOpenBSDForcedDispatcherMayExecOnlyUnveiledWorker$")
	command.Env = append(os.Environ(), "FILEES_SANDBOX_PROBE=allowed-exec")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "filees-exec-ok") {
		t.Fatalf("allowed exec probe: %v: %s", err, output)
	}
}

func expectPledgeDeath(t *testing.T, probe, root string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), "FILEES_SANDBOX_PROBE="+probe, "FILEES_SANDBOX_ROOT="+root)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("pledge probe %s survived: %s", probe, output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState.Success() {
		t.Fatalf("pledge probe %s did not terminate as expected: %v: %s", probe, err, output)
	}
}
