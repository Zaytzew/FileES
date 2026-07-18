package deploy

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestOpenSSHArgsAreClosedAndPinned(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	profile := ServerProfile{ID: "primary", Address: "filees.example.net:2222", KnownHostsPath: knownHosts}
	hostKey, _ := BootstrapAuthorizedKey()
	args, err := OpenSSHArgs(TunnelSpec{
		RemotePort: 42001, ServerProfile: profile,
		HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:32123", HostPublicKey: hostKey}, DeployRequestID: uuid.NewString(), ReconnectPublicKey: hostKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"-l " + TunnelUser, "-T", "-F /dev/null", "StrictHostKeyChecking=yes",
		"HostKeyAlgorithms=ssh-ed25519",
		"UserKnownHostsFile=" + knownHosts, "ExitOnForwardFailure=yes",
		"PubkeyAuthentication=no", "PreferredAuthentications=keyboard-interactive",
		"NumberOfPasswordPrompts=1",
		"-R 127.0.0.1:42001:127.0.0.1:32123", "-p 2222", "filees.example.net",
		TunnelServerCommand,
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
	if args[len(args)-2] != "filees.example.net" || args[len(args)-1] != TunnelServerCommand {
		t.Fatalf("SSH command does not end at configured host and fixed command: %#v", args)
	}
}

func TestOpenSSHArgsRejectNonLoopbackAndInvalidPort(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	profile := ServerProfile{ID: "primary", Address: "filees.example.net:22", KnownHostsPath: knownHosts}
	hostKey, _ := BootstrapAuthorizedKey()
	requestID := uuid.NewString()
	for _, spec := range []TunnelSpec{
		{RemotePort: 0, ServerProfile: profile, DeployRequestID: requestID, ReconnectPublicKey: hostKey, HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:1234", HostPublicKey: hostKey}},
		{RemotePort: 1234, ServerProfile: profile, DeployRequestID: requestID, ReconnectPublicKey: hostKey, HelperEndpoint: HelperEndpoint{Address: "0.0.0.0:1234", HostPublicKey: hostKey}},
		{RemotePort: 1234, ServerProfile: ServerProfile{ID: "bad", Address: "filees.example.net:22", KnownHostsPath: "relative"}, DeployRequestID: requestID, ReconnectPublicKey: hostKey, HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:1234", HostPublicKey: hostKey}},
		{RemotePort: 1234, ServerProfile: profile, DeployRequestID: "invalid", ReconnectPublicKey: hostKey, HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:1234", HostPublicKey: hostKey}},
	} {
		if _, err := OpenSSHArgs(spec); err == nil {
			t.Fatalf("accepted unsafe tunnel spec: %#v", spec)
		}
	}
}
