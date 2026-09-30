//go:build openbsd

package backchannel

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"filees/internal/obsandbox"
)

func TestProtectedDialAfterUnveil(t *testing.T) {
	if path := os.Getenv("FILEES_SANDBOX_SOCKET"); path != "" {
		dial, err := NewUnixDialer(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := obsandbox.Apply(obsandbox.Profile{Name: "backchannel-test", Promises: "stdio rpath unix", Paths: []obsandbox.Path{{Label: "socket", Name: path, Perms: "rwc"}}}); err != nil {
			t.Fatal(err)
		}
		conn, err := dial(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		return
	}
	path := socketPath(t)
	ln, cleanup, err := ListenUnix(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer ln.Close()
	probe := exec.Command(os.Args[0], "-test.run=^TestProtectedDialAfterUnveil$", "-test.v")
	probe.Env = append(os.Environ(), "FILEES_SANDBOX_SOCKET="+path)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("sandbox dial: %s %v", out, err)
	}
}
