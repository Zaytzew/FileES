package deploy

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These three helpers moved out of tunnel_linux.go in phase 0 of
// concepts/WINDOWS_BOOTSTRAP_CONCEPT.md. Their tests move with them, so the
// behaviour the Windows port will depend on is covered on every platform
// rather than only where the FIFO implementation happens to build.

func TestScrubEnvironmentDropsNamedVariables(t *testing.T) {
	got := scrubEnvironment([]string{"PATH=/bin", "SSH_ASKPASS=/evil", "FILEES_ASKPASS_FIFO=/evil", "DISPLAY=:1", "KEEP=yes"},
		"SSH_ASKPASS", "FILEES_ASKPASS_FIFO", "DISPLAY")
	want := []string{"PATH=/bin", "KEEP=yes"}
	if len(got) != len(want) {
		t.Fatalf("scrubEnvironment = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scrubEnvironment = %v, want %v", got, want)
		}
	}
}

// A hostile environment must not survive into the ssh child even when the
// variable carries no value at all.
func TestScrubEnvironmentDropsValuelessEntries(t *testing.T) {
	got := scrubEnvironment([]string{"SSH_ASKPASS", "PATH=/bin"}, "SSH_ASKPASS")
	if len(got) != 1 || got[0] != "PATH=/bin" {
		t.Fatalf("scrubEnvironment = %v, want [PATH=/bin]", got)
	}
}

func TestBoundedDiagnosticTruncatesButReportsFullWrite(t *testing.T) {
	w := &boundedDiagnostic{limit: 8}
	// The writer must claim the whole write, otherwise exec treats the
	// truncation as a short-write error and masks the real ssh failure.
	if n, err := w.Write([]byte("  aaaaaaaaaa")); n != 12 || err != nil {
		t.Fatalf("Write = (%d, %v), want (12, nil)", n, err)
	}
	if n, err := w.Write([]byte("bbbb")); n != 4 || err != nil {
		t.Fatalf("second Write = (%d, %v), want (4, nil)", n, err)
	}
	if got := w.String(); got != "aaaaaa" {
		t.Fatalf("String = %q, want %q", got, "aaaaaa")
	}
}

func TestTunnelCommandErrorKeepsDiagnosticOptional(t *testing.T) {
	base := errors.New("exit status 255")
	withDiagnostic := tunnelCommandError("bootstrap SSH tunnel", base, "permission denied")
	if !errors.Is(withDiagnostic, base) {
		t.Fatal("diagnostic form must keep the wrapped error")
	}
	if got := withDiagnostic.Error(); got != "bootstrap SSH tunnel: exit status 255: permission denied" {
		t.Fatalf("error = %q", got)
	}
	bare := tunnelCommandError("bootstrap SSH tunnel", base, "")
	if !errors.Is(bare, base) {
		t.Fatal("bare form must keep the wrapped error")
	}
	if got := bare.Error(); got != "bootstrap SSH tunnel: exit status 255" {
		t.Fatalf("error = %q", got)
	}
}

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
