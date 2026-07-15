package deploy

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenSSHArgsAreClosedAndPinned(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	args, err := OpenSSHArgs(TunnelSpec{
		RemotePort: 42001, KnownHostsPath: knownHosts,
		HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:32123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"-l " + BootstrapUser, "-N -T", "-F /dev/null", "StrictHostKeyChecking=yes",
		"UserKnownHostsFile=" + knownHosts, "ExitOnForwardFailure=yes",
		"PubkeyAuthentication=no", "NumberOfPasswordPrompts=1",
		"-R 127.0.0.1:42001:127.0.0.1:32123", BootstrapHost,
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("SSH command missing %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"-L", "-D", "ForwardAgent=yes", "ForwardX11=yes"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("SSH command contains forbidden option %q: %s", forbidden, joined)
		}
	}
	if args[len(args)-1] != BootstrapHost {
		t.Fatalf("SSH command does not end at fixed host: %#v", args)
	}
}

func TestOpenSSHArgsRejectNonLoopbackAndInvalidPort(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	for _, spec := range []TunnelSpec{
		{RemotePort: 0, KnownHostsPath: knownHosts, HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:1234"}},
		{RemotePort: 1234, KnownHostsPath: knownHosts, HelperEndpoint: HelperEndpoint{Address: "0.0.0.0:1234"}},
		{RemotePort: 1234, KnownHostsPath: "relative", HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:1234"}},
	} {
		if _, err := OpenSSHArgs(spec); err == nil {
			t.Fatalf("accepted unsafe tunnel spec: %#v", spec)
		}
	}
}
