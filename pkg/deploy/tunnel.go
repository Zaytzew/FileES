package deploy

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	OnboardUser         = "_filees-onboard"
	TunnelUser          = "_filees-tunnel"
	TunnelServerCommand = "filees tunnel-v1"
)

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
