package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures build their known_hosts path with filepath.Join rather than
// writing "/tmp/known_hosts": validate() requires an absolute path, and on
// Windows a leading slash is rooted but drive-relative, so filepath.IsAbs
// rejects it and every case failed before reaching the assertion under test.

func TestServerProfileAddressDefaultsToSSHPort(t *testing.T) {
	for input, wantHost := range map[string]string{"filees.example.net": "filees.example.net", "192.0.2.10": "192.0.2.10", "[2001:db8::1]": "2001:db8::1", "2001:db8::1": "2001:db8::1"} {
		profile := ServerProfile{ID: "server", Address: input, KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
		if err := profile.validate(); err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		host, port := profile.hostAndPort()
		if host != wantHost || port != "22" {
			t.Fatalf("%q => %q:%q", input, host, port)
		}
	}
}

func TestServiceAccessProverUsesNormalizedDefaultPort(t *testing.T) {
	knownHosts := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(knownHosts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	prover, err := NewServiceAccessProver(ServerProfile{ID: "server", Address: "filees.example.net", KnownHostsPath: knownHosts}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if prover.Address != "filees.example.net:22" {
		t.Fatalf("address=%q", prover.Address)
	}
}

func TestServerProfileAddressAcceptsExplicitPort(t *testing.T) {
	for input, wantHost := range map[string]string{"filees.example.net:2222": "filees.example.net", "192.0.2.10:2222": "192.0.2.10", "[2001:db8::1]:2222": "2001:db8::1"} {
		profile := ServerProfile{ID: "server", Address: input, KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
		if err := profile.validate(); err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		host, port := profile.hostAndPort()
		if host != wantHost || port != "2222" {
			t.Fatalf("%q => %q:%q", input, host, port)
		}
	}
}

func TestServerProfileAddressRejectsTransportSyntax(t *testing.T) {
	for _, input := range []string{"svn+ssh://filees.example.net", "user@filees.example.net", "filees.example.net/path", "filees.example.net:0", "filees.example.net:70000", "filees.example.net:abc"} {
		profile := ServerProfile{ID: "server", Address: input, KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
		if err := profile.validate(); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
