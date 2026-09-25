package errmap

import (
	"errors"
	"testing"
)

// The built-in SSH client words connection failures the way Go does. They are
// network conditions, not unknown sync errors; a local daemon socket timeout
// is not (2026-09-25).
func TestBuiltInSSHDialFailuresAreNetworkErrors(t *testing.T) {
	for _, message := range []string{
		"ssh: connect to host demo.filees.space port 22: dial tcp 202.61.192.51:22: i/o timeout",
		"ssh: connect to host cloud.example port 2224: dial tcp 192.0.2.7:2224: connectex: No connection could be made because the target machine actively refused it.",
		"ssh: connect to host demo.filees.space port 22: dial tcp: lookup demo.filees.space: no such host",
		"connect reservation worker: dial tcp: lookup demo.filees.space: no such host",
		"dial tcp 10.0.0.1:22: connectex: A connection attempt failed because the connected party did not properly respond after a period of time, or established connection failed because connected host has failed to respond.",
	} {
		if entry := Classify(errors.New(message)); entry.Code != CodeNetUnreachable {
			t.Errorf("%q: %s %s, want network unreachable", message, entry.Code, entry.Key)
		}
	}
	local := Classify(errors.New(`receive: read unix @->C:\Users\u\.filees\daemon.sock: i/o timeout`))
	if local.Code == CodeNetUnreachable {
		t.Fatal("a local daemon socket timeout was classified as the network")
	}
	dropped := Classify(errors.New("Timeout, server demo.filees.space not responding."))
	if dropped.Key != "net.connection_dropped" {
		t.Fatalf("keepalive expiry: %s %s", dropped.Code, dropped.Key)
	}
}
