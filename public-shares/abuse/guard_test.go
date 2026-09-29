package abuse

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func testGuard(t *testing.T) (*Guard, *time.Time) {
	t.Helper()
	g, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	g.now = func() time.Time { return now }
	return g, &now
}

func failAttempt(t *testing.T, g *Guard, ip netip.Addr) {
	t.Helper()
	done, ok := g.Begin(ip)
	if !ok {
		t.Fatal("attempt refused")
	}
	done(true)
	done(true) // completion is idempotent
}

func TestBanIsolationExpiryAndNoExtension(t *testing.T) {
	g, now := testGuard(t)
	a := netip.MustParseAddr("198.51.100.1")
	b := netip.MustParseAddr("198.51.100.2")
	for i := 0; i < FailureLimit-1; i++ {
		failAttempt(t, g, a)
	}
	if g.Blocked(a) != 0 {
		t.Fatal("early ban")
	}
	finish, ok := g.Begin(a)
	if !ok {
		t.Fatal("success refused")
	}
	finish(false)
	failAttempt(t, g, a)
	if g.Blocked(a) != BlockDuration || g.Blocked(b) != 0 {
		t.Fatal("wrong ban scope")
	}
	*now = now.Add(time.Minute)
	if _, ok := g.Begin(a); ok {
		t.Fatal("banned source admitted")
	}
	if g.Blocked(a) != BlockDuration-time.Minute {
		t.Fatal("ban extended")
	}
	*now = now.Add(BlockDuration - time.Minute)
	if len(g.Snapshot()) != 0 || len(g.sources) != 0 {
		t.Fatal("expired state retained")
	}
	finish, ok = g.Begin(a)
	if !ok {
		t.Fatal("expired ban still enforced")
	}
	finish(false)
}

func TestReservationWindowAndCapacity(t *testing.T) {
	g, now := testGuard(t)
	ip := netip.MustParseAddr("198.51.100.1")
	a, _ := g.Begin(ip)
	b, _ := g.Begin(ip)
	if _, ok := g.Begin(ip); ok {
		t.Fatal("concurrency unbounded")
	}
	*now = now.Add(Window)
	if _, ok := g.Begin(ip); ok {
		t.Fatal("reused expired active window")
	}
	a(true)
	b(true)
	if g.Blocked(ip) != 0 {
		t.Fatal("late failure created ban")
	}
	c, ok := g.Begin(ip)
	if !ok {
		t.Fatal("new window refused")
	}
	c(false)
	for i := 0; i < MaxSources; i++ {
		p := netip.MustParseAddr(fmt.Sprintf("2001:db8::%x", i+1))
		failAttempt(t, g, p)
	}
	if _, ok := g.Begin(ip); ok || len(g.sources) != MaxSources {
		t.Fatal("capacity not enforced")
	}
	*now = now.Add(Window)
	g.mu.Lock()
	g.sweepLocked(*now)
	g.mu.Unlock()
	if len(g.sources) != 0 {
		t.Fatal("idle sweep retained sources")
	}
}

func TestConcurrentAttempts(t *testing.T) {
	g, _ := New(nil)
	ip := netip.MustParseAddr("198.51.100.1")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if done, ok := g.Begin(ip); ok {
				done(true)
			}
			_ = g.Blocked(ip)
			_ = g.Snapshot()
		}()
	}
	wg.Wait()
	// Scheduling may admit only a few of the contending callers. Complete the
	// threshold sequentially rather than assuming a particular goroutine order.
	for i := 0; i < FailureLimit && g.Blocked(ip) == 0; i++ {
		failAttempt(t, g, ip)
	}
	if g.Blocked(ip) == 0 {
		t.Fatal("concurrent failures not blocked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Maintain(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop")
	}
}

func TestSourceTrustBoundary(t *testing.T) {
	g, _ := New([]string{"127.0.0.1"})
	for _, tc := range []struct{ peer, xff, want string }{
		{"198.51.100.1:123", "203.0.113.99", "198.51.100.1"},
		{"192.168.1.1:123", "203.0.113.99", "192.168.1.1"},
		{"[::ffff:198.51.100.1]:123", "", "198.51.100.1"},
		{"127.0.0.1:123", "203.0.113.99", "203.0.113.99"},
		{"127.0.0.1:123", "", ""},
		{"127.0.0.1:123", "203.0.113.99, 198.51.100.1", ""},
		{"127.0.0.1:123", "127.0.0.1", ""},
		{"127.0.0.1:123", "fe80::1%eth0", ""},
		{"not-an-ip", "203.0.113.99", ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.xff)
		ip, ok := g.Source(r)
		if tc.want == "" {
			if ok {
				t.Fatalf("accepted invalid identity: %v", tc)
			}
			continue
		}
		if !ok || ip.String() != tc.want {
			t.Fatalf("source: %v => %v %v", tc, ip, ok)
		}
	}
	for _, bad := range []string{"0.0.0.0/0", "host.example", "fe80::1%eth0"} {
		if _, err := New([]string{bad}); err == nil {
			t.Fatal("bad proxy accepted")
		}
	}
}
