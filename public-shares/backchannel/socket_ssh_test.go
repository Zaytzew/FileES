//go:build !windows

package backchannel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/public-shares/authority"
)

// Opt-in: starts only an isolated sshd on loopback with throwaway fixture keys.
// Neither the host sshd configuration nor authorized_keys is modified.
func TestBackchannelThroughRealOpenSSH(t *testing.T) {
	sshd := os.Getenv("FILEES_TEST_SSHD")
	if sshd == "" {
		t.Skip("set FILEES_TEST_SSHD to an absolute sshd path for the live transport test")
	}
	if !filepath.IsAbs(sshd) {
		t.Fatal("sshd path must be absolute")
	}
	// OpenSSH StrictModes rejects /tmp even with a private sticky child.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".fes-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "authority.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, name := range []string{"host", "client"} {
		out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, name)).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture key: %s %v", out, err)
		}
	}
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	// OpenSSH's channel permission table also gates StreamLocal when TCP is
	// globally disabled. PermitListen none refuses TCP listeners while leaving
	// Unix forwarding available; the negative TCP probe below verifies this.
	config := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey %s/host\nPidFile %s/pid\nAuthorizedKeysFile %s/client.pub\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin prohibit-password\nAllowUsers %s\nAllowTcpForwarding remote\nPermitListen none\nAllowStreamLocalForwarding remote\nStreamLocalBindMask 0177\nStreamLocalBindUnlink no\nAllowAgentForwarding no\nX11Forwarding no\nPermitTTY no\nMaxSessions 0\nForceCommand /usr/bin/false\n", port, dir, dir, dir, account.Username)
	if runtime.GOOS == "linux" {
		config += "UsePAM no\n"
	}
	configPath := filepath.Join(dir, "sshd.conf")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	host, err := os.ReadFile(filepath.Join(dir, "host.pub"))
	if err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte(fmt.Sprintf("[127.0.0.1]:%d %s", port, host)), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "sshd.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	daemon := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", configPath)
	daemon.Stderr = log
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { daemon.Process.Kill(); daemon.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(log.Name())
			t.Fatalf("fixture sshd not listening: %s", raw)
		}
		time.Sleep(25 * time.Millisecond)
	}
	ln, cleanup, err := ListenUnix(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	server := &http.Server{Handler: Server{Authority: stubAuthority{body: "synthetic SSH payload"}}}
	go server.Serve(ln)
	defer server.Close()
	forward := filepath.Join(dir, "forward.sock")
	sshLog, err := os.Create(filepath.Join(dir, "ssh.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer sshLog.Close()
	tunnel := exec.CommandContext(ctx, "ssh", "-F", "none", "-nNT", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "UserKnownHostsFile="+known, "-o", "ExitOnForwardFailure=yes", "-i", filepath.Join(dir, "client"), "-p", fmt.Sprint(port), "-R", forward+":"+path, account.Username+"@127.0.0.1")
	tunnel.Stderr = sshLog
	if err := tunnel.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { tunnel.Process.Kill(); tunnel.Wait() }()
	denyCtx, denyCancel := context.WithTimeout(ctx, 3*time.Second)
	defer denyCancel()
	tcpArgs := append([]string(nil), tunnel.Args[1:]...)
	for i, arg := range tcpArgs {
		if arg == "-R" {
			tcpArgs[i+1] = "127.0.0.1:0:127.0.0.1:9"
		}
	}
	if out, err := exec.CommandContext(denyCtx, "ssh", tcpArgs...).CombinedOutput(); err == nil || denyCtx.Err() != nil || !strings.Contains(string(out), "remote port forwarding failed") {
		t.Fatalf("TCP forwarding was not promptly denied: %s %v", out, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := checkedSocket(forward); err == nil {
			break
		}
		if time.Now().After(deadline) {
			a, _ := os.ReadFile(log.Name())
			b, _ := os.ReadFile(sshLog.Name())
			t.Fatalf("fixture tunnel not ready: %s / %s", a, b)
		}
		time.Sleep(25 * time.Millisecond)
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return DialUnix(ctx, forward) }}
	defer transport.CloseIdleConnections()
	client := Client{BaseURL: "http://authority", HTTP: &http.Client{Transport: transport, Timeout: 3 * time.Second}}
	leaf, err := client.Fetch(ctx, authority.ObjectRequest{Revision: 7})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(leaf.Body)
	leaf.Body.Close()
	if err != nil || string(raw) != "synthetic SSH payload" {
		t.Fatalf("fetch: %q %v", raw, err)
	}
	// No fallback when the destination or the tunnel disappears.
	server.Close()
	if _, err := client.Fetch(ctx, authority.ObjectRequest{Revision: 7}); err == nil {
		t.Fatal("fetch succeeded without authority")
	}
	tunnel.Process.Kill()
	tunnel.Wait()
	if _, err := client.Fetch(ctx, authority.ObjectRequest{Revision: 7}); err == nil {
		t.Fatal("fetch succeeded without tunnel")
	}
}
