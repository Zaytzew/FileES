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
	BootstrapUser = "filees-bootstrap"
	// BootstrapHost is part of the shipped client policy, not deploy input.
	BootstrapHost = "cloud.atmprojekt.pl"
)

type TunnelSpec struct {
	RemotePort     int
	HelperEndpoint HelperEndpoint
	KnownHostsPath string
}

// OpenSSHArgs returns the only outer SSH command shape supported by FileES.
// Host, login and forwarding policy are compiled into the client; an operation
// supplies only its server-assigned loopback port and the local helper endpoint.
func OpenSSHArgs(spec TunnelSpec) ([]string, error) {
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
	knownHosts := filepath.Clean(strings.TrimSpace(spec.KnownHostsPath))
	if knownHosts == "." || !filepath.IsAbs(knownHosts) {
		return nil, errors.New("pinned known_hosts path must be absolute")
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", spec.RemotePort, localPort)
	return []string{
		"-F", "/dev/null",
		"-l", BootstrapUser,
		"-N", "-T",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no",
		"-o", "EnableEscapeCommandline=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "PreferredAuthentications=keyboard-interactive,password",
		"-o", "NumberOfPasswordPrompts=1",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-o", "TCPKeepAlive=no",
		"-o", "LogLevel=ERROR",
		"-R", forward,
		BootstrapHost,
	}, nil
}
