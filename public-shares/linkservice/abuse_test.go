//go:build !windows

package linkservice

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/public-shares/abuse"
)

func TestPrivateAbuseSocketAndLifetime(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	g, _ := abuse.New(nil)
	ip := netip.MustParseAddr("198.51.100.1")
	for i := 0; i < abuse.FailureLimit; i++ {
		done, _ := g.Begin(ip)
		done(true)
	}
	r := Runtime{Abuse: g, Config: Config{Abuse: AbuseConfig{SignalSocket: filepath.Join(root, "signal.sock")}}}
	l, cleanup, err := r.ListenAbuse()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, _ := os.Stat(r.Config.Abuse.SignalSocket)
	if info.Mode().Perm() != 0600 {
		t.Fatal("socket not private")
	}
	if _, _, err := r.ListenAbuse(); err == nil {
		t.Fatal("stole existing socket")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.ServeAbuse(ctx, l); close(done) }()
	c, err := net.DialTimeout("unix", r.Config.Abuse.SignalSocket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(time.Second))
	var bans []abuse.Ban
	err = json.NewDecoder(c).Decode(&bans)
	c.Close()
	if err != nil || len(bans) != 1 || bans[0].IP != ip.String() {
		t.Fatalf("snapshot=%v err=%v", bans, err)
	}
	cancel()
	cleanup()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("socket worker leaked")
	}
	if _, err := os.Stat(r.Config.Abuse.SignalSocket); !os.IsNotExist(err) {
		t.Fatal("socket not removed")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ListenAbuse(); err == nil {
		t.Fatal("public socket directory accepted")
	}
}

func TestAbuseConfigValidation(t *testing.T) {
	base := `{"schema":"filees.public-links/v1","fastcgi":{"network":"unix","address":"@ROOT@/fcgi.sock"},"backchannel":{"network":"unix","address":"@ROOT@/authority.sock"},"visit_key_file":"@KEY@","cache":{"enabled":false}`
	for _, extra := range []string{
		`,"abuse":{"trusted_proxies":["0.0.0.0/0"]}}`,
		`,"abuse":{"signal_socket":"relative.sock"}}`,
		`,"abuse":{"signal_socket":"@ROOT@/fcgi.sock"}}`,
	} {
		if _, err := LoadForCheck(writeConfigFixture(t, base+extra)); err == nil {
			t.Fatal("invalid abuse config accepted")
		}
	}
	r, err := LoadForCheck(writeConfigFixture(t, base+`,"abuse":{"trusted_proxies":["127.0.0.1"],"signal_socket":"@ROOT@/private/signal.sock"}}`))
	if err != nil || r.Abuse == nil {
		t.Fatalf("guard not configured: %v", err)
	}
}
