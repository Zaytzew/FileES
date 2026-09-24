package deploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"filees/pkg/onboarding"
)

// fakeTunnelServer plays the FileES server's side of the activation tunnel
// the way sshd plus "filees tunnel-v1" do: keyboard-interactive for
// _filees-tunnel, a reverse forward, one exec reading the session frame, a
// connection back through the forward, stderr and an exit status.
type fakeTunnelServer struct {
	t        *testing.T
	listener net.Listener
	hostKey  ssh.Signer

	question  string
	accept    func(answer string) bool
	refuseFwd bool
	exitCode  uint32
	stderr    string
	hang      bool // never answer the exec: the client must give up by itself
	silent    bool // stop answering keepalive probes once the command runs

	mu       sync.Mutex
	answers  []string
	command  string
	frame    TunnelSession
	echoed   string
	executed bool
}

func newFakeTunnelServer(t *testing.T) *fakeTunnelServer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return &fakeTunnelServer{t: t, listener: listener, hostKey: signer, question: "One-time code: ",
		accept: func(answer string) bool { return answer == "123456" }}
}

func (s *fakeTunnelServer) serve() {
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	config := &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(meta ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			if meta.User() != TunnelUser {
				return nil, errors.New("wrong user")
			}
			answers, err := challenge("", "", []string{s.question}, []bool{false})
			if err != nil {
				return nil, err
			}
			s.mu.Lock()
			s.answers = append(s.answers, answers...)
			s.mu.Unlock()
			if len(answers) != 1 || !s.accept(answers[0]) {
				return nil, errors.New("refused")
			}
			return nil, nil
		},
	}
	config.AddHostKey(s.hostKey)
	serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	var forwardAddr string
	var forwardPort uint32
	forwarded := make(chan struct{})
	commandRunning := make(chan struct{})
	go func() {
		for request := range requests {
			switch request.Type {
			case "tcpip-forward":
				var payload struct {
					Addr string
					Port uint32
				}
				if err := ssh.Unmarshal(request.Payload, &payload); err != nil || s.refuseFwd {
					_ = request.Reply(false, nil)
					continue
				}
				forwardAddr, forwardPort = payload.Addr, payload.Port
				_ = request.Reply(true, nil)
				close(forwarded)
			case "keepalive@openssh.com":
				select {
				case <-commandRunning:
					if s.silent {
						continue // no reply at all, like a peer that vanished
					}
				default:
				}
				_ = request.Reply(false, nil)
			default:
				_ = request.Reply(false, nil)
			}
		}
	}()
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		for request := range channelRequests {
			if request.Type != "exec" {
				_ = request.Reply(false, nil)
				continue
			}
			var exec struct{ Command string }
			_ = ssh.Unmarshal(request.Payload, &exec)
			s.mu.Lock()
			s.command, s.executed = exec.Command, true
			s.mu.Unlock()
			if s.hang {
				select {} // the client's deadline is the only way out
			}
			_ = request.Reply(true, nil)
			close(commandRunning)
			if s.silent {
				select {}
			}
			frame, _ := DecodeTunnelSession(channel)
			s.mu.Lock()
			s.frame = frame
			s.mu.Unlock()
			select {
			case <-forwarded:
			case <-time.After(5 * time.Second):
			}
			echoed := s.throughForward(serverConn, forwardAddr, forwardPort)
			s.mu.Lock()
			s.echoed = echoed
			s.mu.Unlock()
			if s.stderr != "" {
				_, _ = io.WriteString(channel.Stderr(), s.stderr)
			}
			status := make([]byte, 4)
			binary.BigEndian.PutUint32(status, s.exitCode)
			_, _ = channel.SendRequest("exit-status", false, status)
			_ = channel.Close()
			return
		}
	}
}

// throughForward opens a forwarded-tcpip channel, as sshd does when the
// server's worker connects to the forwarded port, and reads the helper's echo.
func (s *fakeTunnelServer) throughForward(conn ssh.Conn, addr string, port uint32) string {
	payload := ssh.Marshal(struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{addr, port, "127.0.0.1", 40000})
	channel, requests, err := conn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return "open failed: " + err.Error()
	}
	go ssh.DiscardRequests(requests)
	defer channel.Close()
	_, _ = channel.Write([]byte("ping"))
	reply := make([]byte, 4)
	if _, err := io.ReadFull(channel, reply); err != nil {
		return "read failed: " + err.Error()
	}
	return string(reply)
}

// echoHelper stands in for the loopback helper endpoint.
func echoHelper(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return listener.Addr().String()
}

func (s *fakeTunnelServer) spec(t *testing.T, pinned ssh.PublicKey) TunnelSpec {
	t.Helper()
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	address := s.listener.Addr().String()
	host, port, _ := net.SplitHostPort(address)
	line := "[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(pinned))
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	helperKey, _ := BootstrapAuthorizedKey()
	return TunnelSpec{
		RemotePort:         42001,
		HelperEndpoint:     HelperEndpoint{Address: echoHelper(t), HostPublicKey: helperKey},
		DeployRequestID:    uuid.NewString(),
		ReconnectPublicKey: helperKey,
		ServerProfile:      ServerProfile{ID: "probe", Address: address, KnownHostsPath: knownHosts},
	}
}

func runAgainst(t *testing.T, server *fakeTunnelServer, spec TunnelSpec, otp string) error {
	t.Helper()
	go server.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return RunActivationTunnel(ctx, spec, []byte(otp))
}

// The whole exchange the server relies on: the code answers the one question,
// the frame arrives on stdin of "filees tunnel-v1", the server reaches the
// helper through the reverse forward, and exit 0 is success.
func TestActivationTunnelCarriesTheWholeExchange(t *testing.T) {
	server := newFakeTunnelServer(t)
	spec := server.spec(t, server.hostKey.PublicKey())
	if err := runAgainst(t, server, spec, "123456"); err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.command != TunnelServerCommand {
		t.Fatalf("command = %q, want %q", server.command, TunnelServerCommand)
	}
	if server.frame.DeployRequestID != spec.DeployRequestID || server.frame.HelperHostPublicKey != spec.HelperEndpoint.HostPublicKey {
		t.Fatalf("frame = %+v", server.frame)
	}
	if server.echoed != "ping" {
		t.Fatalf("through the forward = %q, want the helper's echo", server.echoed)
	}
}

// StrictHostKeyChecking=yes: a server whose key is not the pinned one never
// sees the one-time code.
func TestActivationTunnelRefusesAnUnpinnedServerBeforeTheCode(t *testing.T) {
	server := newFakeTunnelServer(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	err := runAgainst(t, server, server.spec(t, otherSigner.PublicKey()), "123456")
	if err == nil || !strings.Contains(err.Error(), "Host key verification failed") {
		t.Fatalf("unpinned server: %v, want a host key verification failure", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.answers) != 0 {
		t.Fatalf("the code reached an unverified server: %q", server.answers)
	}
}

// NumberOfPasswordPrompts=1: a refused code is reported in OpenSSH's words
// (pkg/errmap knows them) and never sent a second time.
func TestActivationTunnelSendsARefusedCodeOnce(t *testing.T) {
	server := newFakeTunnelServer(t)
	err := runAgainst(t, server, server.spec(t, server.hostKey.PublicKey()), "999999")
	if err == nil || !strings.Contains(err.Error(), "Permission denied (keyboard-interactive)") {
		t.Fatalf("refused code: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.answers) > 1 {
		t.Fatalf("the code was sent %d times", len(server.answers))
	}
}

// ExitOnForwardFailure=yes: without the reverse forward the tunnel command
// must not run at all.
func TestActivationTunnelStopsWhenTheForwardIsRefused(t *testing.T) {
	server := newFakeTunnelServer(t)
	server.refuseFwd = true
	err := runAgainst(t, server, server.spec(t, server.hostKey.PublicKey()), "123456")
	if err == nil || !strings.Contains(err.Error(), "remote port forwarding failed") {
		t.Fatalf("refused forward: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.executed {
		t.Fatal("the tunnel command ran without its forward")
	}
}

// The server explains itself on stderr - the session-ended marker that
// pkg/errmap maps - and the exit status travels with it.
func TestActivationTunnelReportsExitStatusAndServerStderr(t *testing.T) {
	server := newFakeTunnelServer(t)
	server.exitCode = 3
	server.stderr = "filees-session-ended: lease revoked\n"
	err := runAgainst(t, server, server.spec(t, server.hostKey.PublicKey()), "123456")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "filees-session-ended") {
		t.Fatalf("failed command: %v", err)
	}
}

// The context bounds the call or nothing does. On 2026-09-03 an activation
// outlived its ten-minute deadline inside the OpenSSH tunnel; this one must
// return on its own deadline even when the server never answers the exec.
func TestActivationTunnelHonoursItsDeadline(t *testing.T) {
	server := newFakeTunnelServer(t)
	server.hang = true
	spec := server.spec(t, server.hostKey.PublicKey())
	go server.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunActivationTunnel(ctx, spec, []byte("123456")) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hung server counted as success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the tunnel ignored its context")
	}
}

// ServerAliveCountMax=2: a peer that stops answering is given up with
// OpenSSH's own sentence, which pkg/errmap reads as a dropped connection.
func TestActivationTunnelGivesUpASilentServer(t *testing.T) {
	old := tunnelAliveInterval
	tunnelAliveInterval = 100 * time.Millisecond
	defer func() { tunnelAliveInterval = old }()
	server := newFakeTunnelServer(t)
	server.silent = true
	err := runAgainst(t, server, server.spec(t, server.hostKey.PublicKey()), "123456")
	if err == nil || !strings.Contains(err.Error(), "not responding") {
		t.Fatalf("silent server: %v", err)
	}
}

// Reconnect answers the question with a signature over the server's
// challenge from the durable key; no mail code exists on this path.
func TestReconnectTunnelSignsTheServerChallenge(t *testing.T) {
	server := newFakeTunnelServer(t)
	challenge, err := onboarding.NewReconnectChallenge(bytes.NewReader(bytes.Repeat([]byte("n"), 64)))
	if err != nil {
		t.Fatal(err)
	}
	server.question = challenge
	spec := server.spec(t, server.hostKey.PublicKey())
	identity, err := PrepareReconnectIdentity(t.TempDir(), spec.DeployRequestID, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec.ReconnectPublicKey = identity.PublicKey
	signer, err := loadReconnectSigner(identity.PrivateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := onboarding.EncodeReconnectResponse(challenge, spec.DeployRequestID, signer)
	if err != nil {
		t.Fatal(err)
	}
	server.accept = func(answer string) bool { return answer == want }
	go server.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := RunReconnectTunnel(ctx, spec, identity.PrivateKeyPath); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
}

// Banner rounds pass, one question is answered, and a second question means
// the answer was refused - the secret is never offered twice.
func TestOneQuestionAnswersOnce(t *testing.T) {
	calls := 0
	challenge := oneQuestion(func(string) (string, error) { calls++; return "code", nil })
	if answers, err := challenge("", "banner", nil, nil); err != nil || len(answers) != 0 {
		t.Fatalf("banner round = %v, %v", answers, err)
	}
	if answers, err := challenge("", "", []string{"Code: "}, []bool{false}); err != nil || len(answers) != 1 || answers[0] != "code" {
		t.Fatalf("first question = %v, %v", answers, err)
	}
	if _, err := challenge("", "", []string{"Code: "}, []bool{false}); err == nil {
		t.Fatal("a second question was answered")
	}
	if calls != 1 {
		t.Fatalf("the secret was produced %d times", calls)
	}
}
