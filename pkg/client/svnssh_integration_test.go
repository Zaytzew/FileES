package client

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestSVNSSHRealRoundTrip is an opt-in acceptance test for the complete
// client transport boundary: svn -> OpenSSH -> system sshd -> forced command
// -> svnserve -t. CI without a prepared endpoint skips it.
func TestSVNSSHRealRoundTrip(t *testing.T) {
	url := os.Getenv("FILEES_TEST_SVNSSH_URL")
	identity := os.Getenv("FILEES_TEST_SVNSSH_IDENTITY")
	knownHosts := os.Getenv("FILEES_TEST_SVNSSH_KNOWN_HOSTS")
	port, _ := strconv.Atoi(os.Getenv("FILEES_TEST_SVNSSH_PORT"))
	if url == "" || identity == "" || knownHosts == "" {
		t.Skip("FILEES_TEST_SVNSSH_* acceptance endpoint is not configured")
	}

	cli := New(Options{
		Timeout: 30 * time.Second, LogScope: "test:svnssh",
		SSHIdentityFile: identity, SSHKnownHosts: knownHosts, SSHPort: port,
	})
	ctx := context.Background()
	wc := filepath.Join(t.TempDir(), "wc")
	if out, err := cli.Checkout(ctx, url, wc); err != nil {
		t.Fatalf("checkout: %v\n%s", err, out)
	}
	path := filepath.Join(wc, "s5.txt")
	if err := os.WriteFile(path, []byte("svn+ssh only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := cli.Add(ctx, wc, []string{path}); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := cli.Commit(ctx, wc, []string{path}, "S5 svn+ssh acceptance"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	revision, err := cli.Revision(ctx, url)
	if err != nil || revision != 1 {
		t.Fatalf("revision=%d err=%v, want r1", revision, err)
	}
	status, err := cli.Status(ctx, wc, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range status {
		if entry.Item != "normal" {
			t.Fatalf("dirty status after commit: %#v", status)
		}
	}
	if out, err := cli.Update(ctx, wc); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
}
