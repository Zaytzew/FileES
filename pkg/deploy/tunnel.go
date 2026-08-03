package deploy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"filees/pkg/privatefile"

	"golang.org/x/crypto/ssh"
)

const (
	OnboardUser         = "_filees-onboard"
	TunnelUser          = "_filees-tunnel"
	TunnelServerCommand = "filees tunnel-v1"
)

// Environment names shared by the parent process and the askpass child it
// re-execs. They are part of an internal contract between two instances of
// the same binary, never of the server protocol. The name of the OTP channel
// itself stays platform-specific: it is a FIFO path on Linux and will be a
// named pipe on Windows (concepts/WINDOWS_BOOTSTRAP_CONCEPT.md §4).
const (
	connectKeyEnv       = "FILEES_RECONNECT_KEY"
	connectRequestIDEnv = "FILEES_DEPLOY_REQUEST_ID"
)

// scrubEnvironment drops the named variables from an inherited environment.
// The bootstrap path relies on this so a hostile pre-set SSH_ASKPASS cannot
// survive into the ssh child and redirect the OTP handoff.
func scrubEnvironment(environment []string, names ...string) []string {
	blocked := make(map[string]bool, len(names))
	for _, name := range names {
		blocked[name] = true
	}
	out := environment[:0:0]
	for _, entry := range environment {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		if !blocked[name] {
			out = append(out, entry)
		}
	}
	return out
}

// boundedDiagnostic keeps at most limit bytes of a child's stderr so a
// failing ssh can explain itself without an unbounded buffer.
type boundedDiagnostic struct {
	data  []byte
	limit int
}

func (w *boundedDiagnostic) Write(p []byte) (int, error) {
	wanted := len(p)
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return wanted, nil
}

func (w *boundedDiagnostic) String() string { return strings.TrimSpace(string(w.data)) }

// loadReconnectSigner reads the durable key the server challenges after
// transport loss. It is shared rather than per-platform because the reconnect
// path has no OTP channel to differ over — the secret is the key file itself,
// and "only its owner may read it" is exactly what privatefile expresses. The
// Linux version used to spell that as an explicit 0600 plus a Stat_t uid
// comparison, which is the same rule written in a form Windows cannot honour.
func loadReconnectSigner(path string) (ssh.Signer, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return nil, errors.New("reconnect private key path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("reconnect private key must be a regular file")
	}
	if err := privatefile.Verify(path); err != nil {
		return nil, fmt.Errorf("reconnect private key must be owner-only: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer zero(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("reconnect private key must be unencrypted Ed25519")
	}
	return signer, nil
}

func tunnelCommandError(label string, err error, diagnostic string) error {
	if diagnostic == "" {
		return fmt.Errorf("%s: %w", label, err)
	}
	return fmt.Errorf("%s: %w: %s", label, err, diagnostic)
}

type TunnelSpec struct {
	RemotePort         int
	HelperEndpoint     HelperEndpoint
	DeployRequestID    string
	ReconnectPublicKey string
	ServerProfile      ServerProfile
}

// OpenSSHArgs returns the only outer SSH command shape supported by FileES.
// Login, command and forwarding policy are compiled into the client. The
// installation profile supplies only the endpoint and its pinned host keys.
func OpenSSHArgs(spec TunnelSpec) ([]string, error) {
	if err := spec.ServerProfile.validate(); err != nil {
		return nil, err
	}
	if err := (TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: spec.DeployRequestID, HelperHostPublicKey: spec.HelperEndpoint.HostPublicKey, ReconnectPublicKey: spec.ReconnectPublicKey}).Validate(); err != nil {
		return nil, err
	}
	if spec.RemotePort < 1 || spec.RemotePort > 65535 {
		return nil, errors.New("bootstrap remote port is outside 1..65535")
	}
	host, port, err := net.SplitHostPort(spec.HelperEndpoint.Address)
	if err != nil {
		return nil, fmt.Errorf("helper endpoint: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("reverse tunnel destination must be loopback")
	}
	localPort, err := strconv.Atoi(port)
	if err != nil || localPort < 1 || localPort > 65535 {
		return nil, errors.New("helper endpoint port is invalid")
	}
	knownHosts := filepath.Clean(strings.TrimSpace(spec.ServerProfile.KnownHostsPath))
	if knownHosts == "." || !filepath.IsAbs(knownHosts) {
		return nil, errors.New("pinned known_hosts path must be absolute")
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", spec.RemotePort, localPort)
	serverHost, serverPort := spec.ServerProfile.hostAndPort()
	args := []string{
		"-F", "/dev/null",
		"-p", serverPort,
		"-l", TunnelUser,
		"-T",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "HostKeyAlgorithms=ssh-ed25519",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no",
		"-o", "EnableEscapeCommandline=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "PreferredAuthentications=keyboard-interactive",
		"-o", "NumberOfPasswordPrompts=1",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-o", "TCPKeepAlive=no",
		"-o", "LogLevel=ERROR",
		"-R", forward,
		serverHost,
		TunnelServerCommand,
	}
	return args, nil
}
