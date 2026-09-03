//go:build windows

package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// The context bounds this call or nothing does.
//
// On 2026-09-03 an activation sat past its ten-minute deadline without
// returning: no ssh process remained, no error was logged, and the daemon's
// own timeout did not end it. Everything before the tunnel had run - the
// session file, the reconnect key and the helper host key were all on disk -
// so the call was inside RunOpenSSHTunnel and not observing its context.
//
// The endpoint is a closed port, so ssh fails fast when it behaves; the
// deadline is what catches it when it does not. Failing is the expected result
// here - hanging is the defect.
func TestTheBootstrapTunnelHonoursItsDeadline(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("[127.0.0.1]:1 "+string(ssh.MarshalAuthorizedKey(hostKey))), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := TunnelSpec{
		RemotePort:         42000,
		HelperEndpoint:     HelperEndpoint{Address: "127.0.0.1:1", HostPublicKey: string(ssh.MarshalAuthorizedKey(hostKey))},
		DeployRequestID:    uuid.NewString(),
		ReconnectPublicKey: string(ssh.MarshalAuthorizedKey(hostKey)),
		ServerProfile:      ServerProfile{ID: "probe", Address: "127.0.0.1:1", KnownHostsPath: knownHosts},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- RunOpenSSHTunnel(ctx, spec, []byte("123456")) }()

	select {
	case <-done:
		if elapsed := time.Since(started); elapsed > 25*time.Second {
			t.Fatalf("returned only after %s", elapsed)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("RunOpenSSHTunnel ignored its context: still running twice past the deadline")
	}
}
