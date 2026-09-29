// filees-links-pf is an optional, separately installed OpenBSD administrator
// tool. Run once per minute from root cron; it never changes the PF ruleset.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"filees/public-shares/abuse"
)

const table = "filees_bad_sources"

func main() {
	socket := flag.String("socket", "", "private local links signal socket")
	destination := flag.String("destination", "", "comma-separated public server IPs; state removal targets only these destinations")
	protect := flag.String("protect", "", "required comma-separated IP/CIDR exclusions: management, proxies, trusted networks")
	flag.Parse()
	if runtime.GOOS != "openbsd" {
		fail("OpenBSD only")
	}
	if !filepath.IsAbs(*socket) {
		fail("absolute socket path required")
	}
	destinations, err := parseDestinations(*destination)
	if err != nil {
		fail(err.Error())
	}
	protected, err := parseProtected(*protect)
	if err != nil {
		fail(err.Error())
	}
	for _, dst := range destinations {
		protected = append(protected, netip.PrefixFrom(dst, dst.BitLen()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := reconcile(ctx, *socket, destinations, protected, time.Now(), runPF); err != nil {
		// Never print exec errors, stdout/stderr or the snapshot: they contain IPs.
		fail("reconciliation failed; check service availability and PF configuration")
	}
}

func fail(message string) { fmt.Fprintln(os.Stderr, "filees-links-pf:", message); os.Exit(1) }

type pfRun func(context.Context, string, ...string) error

func runPF(ctx context.Context, input string, args ...string) error {
	cmd := exec.CommandContext(ctx, "/sbin/pfctl", args...)
	cmd.Stdin = strings.NewReader(input)
	// pfctl reports addresses in diagnostics; do not inherit output streams.
	return cmd.Run()
}

func safePublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.Zone() == "" && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

func parseDestinations(text string) ([]netip.Addr, error) {
	var result []netip.Addr
	for _, value := range strings.Split(text, ",") {
		ip, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || !safePublic(ip) {
			return nil, errors.New("public unscoped destination IPs required")
		}
		result = append(result, ip.Unmap())
	}
	if len(result) > 8 {
		return nil, errors.New("at most eight destination IPs allowed")
	}
	return result, nil
}

func parseProtected(text string) ([]netip.Prefix, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("explicit protected addresses/networks required")
	}
	var out []netip.Prefix
	for _, item := range strings.Split(text, ",") {
		item = strings.TrimSpace(item)
		p, err := netip.ParsePrefix(item)
		if err != nil {
			ip, e := netip.ParseAddr(item)
			if e != nil || ip.Zone() != "" {
				return nil, errors.New("invalid protected address/network")
			}
			ip = ip.Unmap()
			p = netip.PrefixFrom(ip, ip.BitLen())
		}
		if p.Addr().Is4In6() {
			return nil, errors.New("use IPv4 notation for protected IPv4 networks")
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func readSnapshot(ctx context.Context, socket string) ([]abuse.Ban, error) {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, errors.New("signal unavailable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	const max = 512 * 1024
	raw, err := io.ReadAll(io.LimitReader(conn, max+1))
	if err != nil || len(raw) > max {
		return nil, errors.New("invalid signal size")
	}
	var bans []abuse.Ban
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bans); err != nil || len(bans) > abuse.MaxSources {
		return nil, errors.New("invalid signal")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing signal data")
	}
	return bans, nil
}

func allowedBans(bans []abuse.Ban, protected []netip.Prefix, now time.Time) ([]string, error) {
	seen := make(map[string]bool)
	for _, ban := range bans {
		ip, err := netip.ParseAddr(ban.IP)
		if err != nil || ip.Zone() != "" {
			return nil, errors.New("invalid source")
		}
		ip = ip.Unmap()
		until := time.Unix(ban.Until, 0)
		// A ban may be created while the local snapshot is being read (the
		// socket deadline is two seconds). Do not reject the whole snapshot
		// merely because its ten-minute interval starts just after our clock.
		if until.After(now.Add(abuse.BlockDuration + 2*time.Second)) {
			return nil, errors.New("invalid expiry")
		}
		if !until.After(now) || !safePublic(ip) {
			continue
		}
		excluded := false
		for _, p := range protected {
			if p.Contains(ip) {
				excluded = true
				break
			}
		}
		if !excluded {
			seen[ip.String()] = true
		}
	}
	result := make([]string, 0, len(seen))
	for ip := range seen {
		result = append(result, ip)
	}
	sort.Strings(result)
	return result, nil
}

func reconcile(ctx context.Context, socket string, destinations []netip.Addr, protected []netip.Prefix, now time.Time, run pfRun) error {
	bans, signalErr := readSnapshot(ctx, socket)
	var ips []string
	if signalErr == nil {
		ips, signalErr = allowedBans(bans, protected, now)
	}
	// A bad/missing signal clears THIS dedicated table. No permanent block when
	// links restarts; no disks or stale snapshot fallback. Other PF tables stay put.
	if signalErr != nil {
		ips = nil
	}
	input := strings.Join(ips, "\n")
	if input != "" {
		input += "\n"
	}
	if err := run(ctx, input, "-t", table, "-T", "replace", "-f", "-"); err != nil {
		return errors.New("table update failed")
	}
	if signalErr != nil {
		return signalErr
	}
	for _, ip := range ips {
		// Not '-k IP' alone: that would kill traffic to unrelated destinations.
		for _, dst := range destinations {
			if netip.MustParseAddr(ip).Is4() != dst.Is4() {
				continue
			}
			if err := run(ctx, "", "-k", ip, "-k", dst.String()); err != nil {
				return errors.New("state removal failed")
			}
		}
	}
	return nil
}
