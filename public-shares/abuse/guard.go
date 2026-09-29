// Package abuse bounds password failures by source, not by public share.
// All source data is process-local and expires; nothing is logged or persisted.
package abuse

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	FailureLimit  = 12
	Window        = 10 * time.Minute
	BlockDuration = 10 * time.Minute
	MaxSources    = 4096
)

type source struct {
	until, blocked   time.Time
	failures, active int
}

type Guard struct {
	mu      sync.Mutex
	sources map[netip.Addr]*source
	proxies map[netip.Addr]bool
	now     func() time.Time
}

// New accepts exact proxy addresses only. The trusted proxy MUST discard the
// incoming X-Forwarded-For and emit one client address, not append to a chain.
func New(trustedProxies []string) (*Guard, error) {
	g := &Guard{sources: make(map[netip.Addr]*source), proxies: make(map[netip.Addr]bool), now: time.Now}
	for _, text := range trustedProxies {
		ip, err := netip.ParseAddr(text)
		if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, errors.New("trusted_proxies requires exact unscoped IP addresses")
		}
		g.proxies[ip.Unmap()] = true
	}
	return g, nil
}

func (g *Guard) Source(r *http.Request) (netip.Addr, bool) {
	text := r.RemoteAddr
	if host, _, err := net.SplitHostPort(text); err == nil {
		text = host
	}
	ip, err := netip.ParseAddr(text)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if g.proxies[ip] {
		values := r.Header.Values("X-Forwarded-For")
		if len(values) != 1 {
			return netip.Addr{}, false
		}
		ip, err = netip.ParseAddr(strings.TrimSpace(values[0]))
		if err != nil || ip.Zone() != "" {
			return netip.Addr{}, false
		}
		ip = ip.Unmap()
		if g.proxies[ip] {
			return netip.Addr{}, false
		}
	}
	return ip, ip.IsValid() && !ip.IsUnspecified() && !ip.IsMulticast()
}

// Blocked never allocates for ordinary visitors. Denied traffic does not extend
// the deadline, and expired entries are pruned even without further traffic.
func (g *Guard) Blocked(ip netip.Addr) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.sources[ip]; s != nil && s.blocked.After(g.now()) {
		return s.blocked.Sub(g.now())
	}
	return 0
}

// Begin limits one source to two concurrent verifiers. The completion owns the
// entry it reserved, so a late result cannot debit a newer window. Call it on
// every path (including saturation/cancellation); only a verified mismatch counts.
func (g *Guard) Begin(ip netip.Addr) (finish func(bool), ok bool) {
	g.mu.Lock()
	now := g.now()
	s := g.sources[ip]
	if s != nil && !s.until.After(now) && s.active > 0 {
		g.mu.Unlock()
		return nil, false
	}
	if s != nil && !s.until.After(now) && s.active == 0 {
		delete(g.sources, ip)
		s = nil
	}
	if s == nil {
		if len(g.sources) >= MaxSources {
			g.sweepLocked(now)
		}
		if len(g.sources) >= MaxSources {
			g.mu.Unlock()
			return nil, false
		}
		s = &source{until: now.Add(Window)}
		g.sources[ip] = s
	}
	if s.blocked.After(now) || s.active >= 2 {
		g.mu.Unlock()
		return nil, false
	}
	s.active++
	g.mu.Unlock()
	var once sync.Once
	return func(failed bool) {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			s.active--
			now := g.now()
			if g.sources[ip] != s || !s.until.After(now) {
				return
			}
			if failed && s.blocked.IsZero() {
				s.failures++
				if s.failures >= FailureLimit {
					s.blocked = now.Add(BlockDuration)
					s.until = s.blocked
				}
			}
			if s.active == 0 && s.failures == 0 {
				delete(g.sources, ip)
			}
		})
	}, true
}

func (g *Guard) sweepLocked(now time.Time) {
	for ip, s := range g.sources {
		if !s.until.After(now) && s.active == 0 {
			delete(g.sources, ip)
		}
	}
}

// Maintain runs under the service lifecycle; no unowned background goroutine.
func (g *Guard) Maintain(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.mu.Lock()
			g.sweepLocked(g.now())
			g.mu.Unlock()
		}
	}
}

type Ban struct {
	IP    string `json:"ip"`
	Until int64  `json:"until"`
}

// Snapshot is ONLY for the private, local administrative socket, never HTTP
// public routes, logs or disk. The PF consumer must independently validate it.
func (g *Guard) Snapshot() []Ban {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.sweepLocked(now)
	bans := make([]Ban, 0)
	for ip, s := range g.sources {
		if s.blocked.After(now) {
			bans = append(bans, Ban{IP: ip.String(), Until: s.blocked.Unix()})
		}
	}
	return bans
}
