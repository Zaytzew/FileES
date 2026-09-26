//go:build !windows

package activation

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/serveralerts"
	"filees/pkg/onboarding"
)

func TestAlertMailboxSVNAccessIsolation(t *testing.T) {
	mucc, err := exec.LookPath("svnmucc")
	if err != nil {
		t.Skip("svnmucc unavailable")
	}
	look, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook unavailable")
	}
	m, cfg := newActivationTestManager(t)
	a := testActivationGrant(t, time.Now().Add(time.Hour))
	b := testActivationGrant(t, time.Now().Add(time.Hour))
	phone := testActivationGrant(t, time.Now().Add(time.Hour))
	staged := testActivationGrant(t, time.Now().Add(time.Hour))
	phone.RealmID = a.RealmID
	phone.Kind = onboarding.KindMobile
	staged.RealmID = a.RealmID
	m.config.MobileEntryPath = filepath.Join(t.TempDir(), "mobile-entry")
	for _, g := range []onboarding.ActivationGrant{a, b, phone, staged} {
		if err := m.Stage(g); err != nil {
			t.Fatal(err)
		}
		if g.ClientID != staged.ClientID {
			if err := m.RecordProof(g.OperationID, g.ClientID); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Publish(context.Background(), g); err != nil {
				t.Fatal(err)
			}
		}
	}
	pub := serveralerts.Publisher{Repository: cfg.ServiceRepository, SVNLook: look, SVNMucc: mucc, TempDir: t.TempDir()}
	for _, g := range []onboarding.ActivationGrant{a, b} {
		if _, err := pub.Publish(context.Background(), g.RealmID, "disk", "error", "active", "private-"+g.RealmID); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.RefreshAlertAccess(); err != nil {
		t.Fatal(err)
	}
	passwd := "[users]\n"
	for _, g := range []onboarding.ActivationGrant{a, b, phone, staged} {
		passwd += g.ClientID + " = test\n"
	}
	conf := filepath.Join(cfg.ServiceRepository, "conf")
	if err := os.WriteFile(filepath.Join(conf, "passwd"), []byte(passwd), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "svnserve.conf"), []byte("[general]\nanon-access = none\nauth-access = write\npassword-db = passwd\nauthz-db = "+cfg.AuthzFile+"\nrealm = alert-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cmd := exec.Command(cfg.SVNServeBinary, "-d", "--foreground", "--listen-host", "127.0.0.1", "--listen-port", fmt.Sprint(port), "-r", cfg.ServiceRepository)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 100; i++ {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 20*time.Millisecond)
		if e == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	base := fmt.Sprintf("svn://127.0.0.1:%d", port)
	run := func(user string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		args = append(args, "--non-interactive", "--no-auth-cache", "--username", user, "--password", "test")
		out, e := exec.CommandContext(ctx, cfg.SVNBinary, args...).CombinedOutput()
		return string(out), e
	}
	own := base + "/alerts/" + a.RealmID
	if out, e := run(a.ClientID, "cat", own+"/snapshot.json"); e != nil || !strings.Contains(out, "private-"+a.RealmID) {
		t.Fatal(out, e)
	}
	for _, user := range []string{b.ClientID, phone.ClientID, staged.ClientID} {
		for _, verb := range []string{"cat", "log"} {
			target := own
			if verb == "cat" {
				target += "/snapshot.json"
			}
			if out, e := run(user, verb, target); e == nil {
				t.Fatalf("%s %s allowed: %s", user, verb, out)
			}
		}
	}
	if out, e := run(a.ClientID, "mkdir", own+"/unauthorized", "-m", "bad"); e == nil {
		t.Fatal("client wrote mailbox", out)
	}
	if out, e := run(a.ClientID, "log", own); e != nil || strings.Contains(out, "private-") {
		t.Fatal("log leaked payload", out, e)
	}
	if _, err := m.Revoke(context.Background(), a.ClientID, "test"); err != nil {
		t.Fatal(err)
	}
	if out, e := run(a.ClientID, "cat", own+"/snapshot.json", "-r", "HEAD"); e == nil {
		t.Fatal("revoked client read", out)
	}
}
