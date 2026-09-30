//go:build !windows

package backchannel

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// An explicit root-only acceptance checks the kernel, not just our validator.
func TestProtectedSocketDeniesForeignUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to launch an unprivileged probe")
	}
	account, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 {
		t.Fatalf("invalid foreign UID: %v", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	path := socketPath(t)
	ln, cleanup, err := ListenUnix(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer ln.Close()
	probe := exec.Command(os.Args[0], "-test.run=^TestSocketForeignUIDProbe$", "-test.v")
	probe.Env = append(os.Environ(), "FILEES_FOREIGN_SOCKET="+path)
	probe.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}}
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("foreign UID probe: %s %v", out, err)
	}
}

func TestSocketForeignUIDProbe(t *testing.T) {
	path := os.Getenv("FILEES_FOREIGN_SOCKET")
	if path == "" {
		t.Skip("subprocess only")
	}
	if os.Geteuid() == 0 {
		t.Fatal("probe still root")
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("want kernel EACCES, got %v", err)
	}
}
