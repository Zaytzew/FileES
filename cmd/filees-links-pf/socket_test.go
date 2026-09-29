//go:build !windows

package main

import (
	"context"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReconcilePrivateSocketCommands(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		payload string
		valid   bool
	}{
		{`[{"ip":"198.51.100.1","until":1700000060}]`, true},
		{`[{"ip":"0.0.0.0/0","until":1700000060}]`, false},
		{`[] {}`, false},
		{`[{"ip":"198.51.100.1","until":1700000060,"command":"anything"}]`, false},
	} {
		path := filepath.Join(t.TempDir(), "signal.sock")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			c, e := l.Accept()
			if e == nil {
				_ = c.SetDeadline(time.Now().Add(time.Second))
				_, _ = io.WriteString(c, tc.payload)
				c.Close()
			}
		}()
		var calls [][]string
		var inputs []string
		err = reconcile(context.Background(), path, []netip.Addr{netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("2001:db8::10")}, nil, now, func(_ context.Context, input string, args ...string) error {
			calls = append(calls, args)
			inputs = append(inputs, input)
			return nil
		})
		l.Close()
		<-done
		if tc.valid {
			if err != nil || len(calls) != 2 || inputs[0] != "198.51.100.1\n" || !reflect.DeepEqual(calls[1], []string{"-k", "198.51.100.1", "-k", "203.0.113.1"}) {
				t.Fatalf("commands=%v err=%v", calls, err)
			}
		} else if err == nil || len(calls) != 1 || inputs[0] != "" {
			t.Fatalf("unsafe signal: calls=%v err=%v", calls, err)
		}
	}
}
