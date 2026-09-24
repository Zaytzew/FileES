package deploy

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

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

func TestResolvedTunnelIsPinnedAndLoopback(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	profile := ServerProfile{ID: "primary", Address: "filees.example.net:2222", KnownHostsPath: knownHosts}
	hostKey, _ := BootstrapAuthorizedKey()
	got, err := resolveTunnel(TunnelSpec{
		RemotePort: 42001, ServerProfile: profile,
		HelperEndpoint: HelperEndpoint{Address: "127.0.0.1:32123", HostPublicKey: hostKey}, DeployRequestID: uuid.NewString(), ReconnectPublicKey: hostKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := tunnelEndpoints{server: "filees.example.net:2222", host: "filees.example.net", port: "2222", remote: "127.0.0.1:42001", helper: "127.0.0.1:32123", knownHosts: knownHosts}
	if got != want {
		t.Fatalf("resolveTunnel = %+v, want %+v", got, want)
	}
}
func TestResolveTunnelRejectsNonLoopbackAndInvalidPort(t *testing.T) {
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
		if _, err := resolveTunnel(spec); err == nil {
			t.Fatalf("accepted unsafe tunnel spec: %#v", spec)
		}
	}
}
