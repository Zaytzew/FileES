package main

import (
	"context"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"filees/public-shares/abuse"
)

func TestSignalValidation(t *testing.T) {
	now := time.Unix(1700000000, 0)
	protected, err := parseProtected("203.0.113.0/24,198.51.100.2,2001:db8::2")
	if err != nil {
		t.Fatal(err)
	}
	var bans []abuse.Ban
	for _, ip := range []string{"198.51.100.1", "::ffff:198.51.100.1", "198.51.100.2", "203.0.113.5", "127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "2001:db8::1", "2001:db8::2"} {
		bans = append(bans, abuse.Ban{IP: ip, Until: now.Add(time.Minute).Unix()})
	}
	ips, err := allowedBans(bans, protected, now)
	if err != nil || !reflect.DeepEqual(ips, []string{"198.51.100.1", "2001:db8::1"}) {
		t.Fatalf("ips=%v err=%v", ips, err)
	}
	for _, bad := range []abuse.Ban{
		{IP: "198.51.100.1/24", Until: now.Unix() + 1},
		{IP: "198.51.100.1\n0.0.0.0/0", Until: now.Unix() + 1},
		{IP: "198.51.100.1", Until: now.Add(time.Hour).Unix()},
	} {
		if _, err := allowedBans([]abuse.Ban{bad}, protected, now); err == nil {
			t.Fatal("bad signal accepted")
		}
	}
}

func TestMissingSignalClearsOnlyDedicatedTable(t *testing.T) {
	var calls [][]string
	err := reconcile(context.Background(), "missing-socket", []netip.Addr{netip.MustParseAddr("203.0.113.1")}, nil, time.Now(), func(_ context.Context, input string, args ...string) error {
		if input != "" {
			t.Fatal("stale bans retained")
		}
		calls = append(calls, args)
		return nil
	})
	if err == nil || len(calls) != 1 || !reflect.DeepEqual(calls[0], []string{"-t", table, "-T", "replace", "-f", "-"}) {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
}
