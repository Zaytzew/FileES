package deploy

import "testing"

func TestServerProfileAddressDefaultsToSSHPort(t *testing.T) {
	for input, wantHost := range map[string]string{"filees.example.net": "filees.example.net", "192.0.2.10": "192.0.2.10", "[2001:db8::1]": "2001:db8::1", "2001:db8::1": "2001:db8::1"} {
		profile := ServerProfile{ID: "server", Address: input, KnownHostsPath: "/tmp/known_hosts"}
		if err := profile.validate(); err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		host, port := profile.hostAndPort()
		if host != wantHost || port != "22" {
			t.Fatalf("%q => %q:%q", input, host, port)
		}
	}
}

func TestServerProfileAddressAcceptsExplicitPort(t *testing.T) {
	for input, wantHost := range map[string]string{"filees.example.net:2222": "filees.example.net", "192.0.2.10:2222": "192.0.2.10", "[2001:db8::1]:2222": "2001:db8::1"} {
		profile := ServerProfile{ID: "server", Address: input, KnownHostsPath: "/tmp/known_hosts"}
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
		profile := ServerProfile{ID: "server", Address: input, KnownHostsPath: "/tmp/known_hosts"}
		if err := profile.validate(); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
