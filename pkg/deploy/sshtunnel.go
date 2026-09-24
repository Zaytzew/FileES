package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/onboarding"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// The activation tunnel used to be the system OpenSSH client, answering
// keyboard-interactive through an askpass re-exec of this binary. A clean Windows showed what that cost (2026-09-24): no OpenSSH
// at all, an askpass that Windows refused to start from a Store package, and
// options older OpenSSH releases reject. The tunnel is now this code, in the
// daemon's own process. It keeps that invocation's policy option for option:
//
//	-l _filees-tunnel, -T, one exec of "filees tunnel-v1" with the session
//	  frame on stdin              -> tunnelUser, session.Run
//	StrictHostKeyChecking=yes, HostKeyAlgorithms=ssh-ed25519,
//	  UserKnownHostsFile=pinned, GlobalKnownHostsFile=/dev/null
//	                              -> knownhosts over the pinned file only
//	PubkeyAuthentication=no, PreferredAuthentications=keyboard-interactive,
//	  NumberOfPasswordPrompts=1   -> one answered question, then refusal
//	-R 127.0.0.1:remote:127.0.0.1:helper, ExitOnForwardFailure=yes
//	                              -> client.Listen before the command runs
//	ServerAliveInterval=15, ServerAliveCountMax=2, TCPKeepAlive=no
//	                              -> keepalive@openssh.com probes
//	ForwardAgent/X11/LocalCommand/escape characters
//	                              -> not implemented, so nothing to disable
//
// The secret - the mail OTP or the signature over the reconnect challenge -
// never leaves this process: no pipe, no environment, no child.

const (
	tunnelAliveCountMax = 2
	tunnelDialTimeout   = 30 * time.Second
)

// tunnelAliveInterval is ServerAliveInterval; a variable only so a test can
// watch a dead peer being given up without waiting half a minute.
var tunnelAliveInterval = 15 * time.Second

// tunnelAnswer answers the server's single keyboard-interactive question:
// the OTP on bootstrap, a signature over the challenge on reconnect.
type tunnelAnswer func(prompt string) (string, error)

// tunnelEndpoints is a validated TunnelSpec in the form the client needs.
type tunnelEndpoints struct {
	server     string // host:port of the FileES server
	host       string // for messages, as OpenSSH names it
	port       string
	remote     string // 127.0.0.1:RemotePort on the server
	helper     string // loopback helper endpoint on this machine
	knownHosts string
}

// resolveTunnel checks a spec against the one tunnel shape FileES supports.
// Login, command and forwarding policy are compiled in; the installation
// profile supplies only the endpoint and its pinned host keys.
func resolveTunnel(spec TunnelSpec) (tunnelEndpoints, error) {
	if err := spec.ServerProfile.validate(); err != nil {
		return tunnelEndpoints{}, err
	}
	if err := (TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: spec.DeployRequestID, HelperHostPublicKey: spec.HelperEndpoint.HostPublicKey, ReconnectPublicKey: spec.ReconnectPublicKey}).Validate(); err != nil {
		return tunnelEndpoints{}, err
	}
	if spec.RemotePort < 1 || spec.RemotePort > 65535 {
		return tunnelEndpoints{}, errors.New("bootstrap remote port is outside 1..65535")
	}
	helperHost, helperPort, err := net.SplitHostPort(spec.HelperEndpoint.Address)
	if err != nil {
		return tunnelEndpoints{}, fmt.Errorf("helper endpoint: %w", err)
	}
	ip := net.ParseIP(helperHost)
	if ip == nil || !ip.IsLoopback() {
		return tunnelEndpoints{}, errors.New("reverse tunnel destination must be loopback")
	}
	if port, err := strconv.Atoi(helperPort); err != nil || port < 1 || port > 65535 {
		return tunnelEndpoints{}, errors.New("helper endpoint port is invalid")
	}
	knownHosts := filepath.Clean(strings.TrimSpace(spec.ServerProfile.KnownHostsPath))
	if knownHosts == "." || !filepath.IsAbs(knownHosts) {
		return tunnelEndpoints{}, errors.New("pinned known_hosts path must be absolute")
	}
	host, port := spec.ServerProfile.hostAndPort()
	return tunnelEndpoints{
		server:     net.JoinHostPort(host, port),
		host:       host,
		port:       port,
		remote:     net.JoinHostPort("127.0.0.1", strconv.Itoa(spec.RemotePort)),
		helper:     spec.HelperEndpoint.Address,
		knownHosts: knownHosts,
	}, nil
}

// runSSHTunnel brings up the activation tunnel and returns when the server's
// tunnel command ends, the connection dies or ctx is cancelled. label names
// the tunnel in errors the way tunnelCommandError always has.
func runSSHTunnel(ctx context.Context, label string, spec TunnelSpec, answer tunnelAnswer) error {
	endpoints, err := resolveTunnel(spec)
	if err != nil {
		return err
	}
	frame, err := EncodeTunnelSession(TunnelSession{Schema: TunnelSessionSchema, DeployRequestID: spec.DeployRequestID, HelperHostPublicKey: spec.HelperEndpoint.HostPublicKey, ReconnectPublicKey: spec.ReconnectPublicKey})
	if err != nil {
		return err
	}
	pinned, err := knownhosts.New(endpoints.knownHosts)
	if err != nil {
		return fmt.Errorf("%s: pinned known_hosts: %w", label, err)
	}
	config := &ssh.ClientConfig{
		User:              TunnelUser,
		Auth:              []ssh.AuthMethod{ssh.KeyboardInteractive(oneQuestion(answer))},
		HostKeyCallback:   pinned,
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           tunnelDialTimeout,
	}

	dialer := net.Dialer{Timeout: tunnelDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", endpoints.server)
	if err != nil {
		return tunnelCommandError(label, fmt.Errorf("ssh: connect to host %s port %s: %w", endpoints.host, endpoints.port, err), "")
	}
	_ = conn.SetDeadline(time.Now().Add(tunnelDialTimeout))
	clientConn, channels, requests, err := ssh.NewClientConn(conn, endpoints.server, config)
	if err != nil {
		_ = conn.Close()
		return tunnelCommandError(label, handshakeError(endpoints, err), "")
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(clientConn, channels, requests)
	defer client.Close()

	// cause is why this side closed the connection, read after the session.
	var causeMu sync.Mutex
	var cause error
	closeWith := func(err error) {
		causeMu.Lock()
		first := cause == nil
		if first {
			cause = err
		}
		causeMu.Unlock()
		if first {
			_ = client.Close()
		}
	}
	closedBecause := func() error {
		causeMu.Lock()
		defer causeMu.Unlock()
		return cause
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			closeWith(ctx.Err())
		case <-stop:
		}
	}()
	go keepAlive(client, stop, func() {
		closeWith(fmt.Errorf("Timeout, server %s not responding.", endpoints.host))
	})

	listener, err := client.Listen("tcp", endpoints.remote)
	if err != nil {
		return tunnelCommandError(label, fmt.Errorf("Error: remote port forwarding failed for listen port %d: %w", spec.RemotePort, err), "")
	}
	defer listener.Close()
	go forwardToHelper(listener, endpoints.helper)

	session, err := client.NewSession()
	if err != nil {
		return tunnelCommandError(label, tunnelCause(closedBecause(), err), "")
	}
	defer session.Close()
	diagnostic := &boundedDiagnostic{limit: 16 * 1024}
	session.Stdin = bytes.NewReader(frame)
	session.Stdout = io.Discard
	session.Stderr = diagnostic
	if err := session.Run(TunnelServerCommand); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			err = fmt.Errorf("exit status %d", exit.ExitStatus())
		}
		return tunnelCommandError(label, tunnelCause(closedBecause(), err), diagnostic.String())
	}
	return nil
}

// oneQuestion is NumberOfPasswordPrompts=1: rounds without questions (banners,
// instructions) pass, the first question is answered, and a second question
// means the answer was refused - never a retry with the same secret.
func oneQuestion(answer tunnelAnswer) ssh.KeyboardInteractiveChallenge {
	var mu sync.Mutex
	answered := false
	return func(_, _ string, questions []string, _ []bool) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(questions) == 0 {
			return nil, nil
		}
		if answered || len(questions) != 1 {
			return nil, errors.New("Permission denied (keyboard-interactive).")
		}
		answered = true
		reply, err := answer(questions[0])
		if err != nil {
			return nil, err
		}
		return []string{reply}, nil
	}
}

// handshakeError words a failed handshake the way OpenSSH did, because
// pkg/errmap and the people reading activation details know those words.
func handshakeError(endpoints tunnelEndpoints, err error) error {
	var keyErr *knownhosts.KeyError
	var revoked *knownhosts.RevokedError
	switch {
	case errors.As(err, &keyErr) && len(keyErr.Want) == 0:
		return fmt.Errorf("No ED25519 host key is known for %s and you have requested strict checking. Host key verification failed: %w", endpoints.host, err)
	case errors.As(err, &keyErr), errors.As(err, &revoked):
		return fmt.Errorf("REMOTE HOST IDENTIFICATION HAS CHANGED for %s. Host key verification failed: %w", endpoints.host, err)
	case bytes.Contains([]byte(err.Error()), []byte("unable to authenticate")):
		return fmt.Errorf("%s: Permission denied (keyboard-interactive): %w", endpoints.host, err)
	default:
		return err
	}
}

// tunnelCause prefers the reason this side closed the connection (keepalive
// timeout, cancellation) over the error the closing produced.
func tunnelCause(cause, err error) error {
	if cause != nil {
		return cause
	}
	return err
}

// keepAlive is ServerAliveInterval=15 with ServerAliveCountMax=2: a probe
// every interval, and the connection is given up after two in a row went
// unanswered. Any reply counts, including a refusal of the request itself.
func keepAlive(client *ssh.Client, stop <-chan struct{}, dead func()) {
	ticker := time.NewTicker(tunnelAliveInterval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		replied := make(chan struct{})
		go func() {
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err == nil {
				close(replied)
			}
		}()
		select {
		case <-stop:
			return
		case <-replied:
			missed = 0
		case <-time.After(tunnelAliveInterval):
			missed++
			if missed >= tunnelAliveCountMax {
				dead()
				return
			}
		}
	}
}

// forwardToHelper carries every connection the server opens on the forwarded
// port to the loopback helper, until the listener closes with the tunnel.
func forwardToHelper(listener net.Listener, helper string) {
	for {
		remote, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer remote.Close()
			local, err := net.DialTimeout("tcp", helper, tunnelDialTimeout)
			if err != nil {
				return
			}
			defer local.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
			go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
			<-done
		}()
	}
}

// RunActivationTunnel brings up the bootstrap tunnel, answering the server's
// question with the one-time code from the mail.
func RunActivationTunnel(ctx context.Context, spec TunnelSpec, otp []byte) error {
	if len(otp) == 0 || len(otp) > 1024 || bytes.Contains(otp, []byte{'\r'}) || bytes.Contains(otp, []byte{'\n'}) {
		return errors.New("bootstrap OTP must contain 1..1024 bytes without newlines")
	}
	secret := append([]byte(nil), otp...)
	defer zero(secret)
	return runSSHTunnel(ctx, "bootstrap SSH tunnel", spec, func(string) (string, error) {
		return string(secret), nil
	})
}

// RunReconnectTunnel recreates the same tunnel after transport loss. The
// server challenges the durable reconnect key; the mail OTP is neither
// retained nor reused, and the question is answered with a signature over
// the challenge.
func RunReconnectTunnel(ctx context.Context, spec TunnelSpec, privateKeyPath string) error {
	signer, err := loadReconnectSigner(privateKeyPath)
	if err != nil {
		return err
	}
	configured, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(spec.ReconnectPublicKey)))
	if err != nil || !bytes.Equal(configured.Marshal(), signer.PublicKey().Marshal()) {
		return errors.New("reconnect private key does not match tunnel binding")
	}
	return runSSHTunnel(ctx, "reconnect SSH tunnel", spec, func(challenge string) (string, error) {
		return onboarding.EncodeReconnectResponse(challenge, spec.DeployRequestID, signer)
	})
}
