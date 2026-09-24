package sshexec

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSVNServer answers one svn+ssh style exec: it checks the client key,
// echoes one line of stdin back and exits with a chosen status - while the
// client's stdin stays open, the way Subversion leaves it.
type fakeSVNServer struct {
	listener net.Listener
	hostKey  ssh.Signer
	allowed  ssh.PublicKey
	exitCode uint32
	silent   bool // accept the exec, then stop answering keepalives

	mu      sync.Mutex
	user    string
	command string
}

func newFakeSVNServer(t *testing.T, allowed ssh.PublicKey) *fakeSVNServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewSignerFromKey(private)
	return &fakeSVNServer{listener: listener, hostKey: hostKey, allowed: allowed}
}

func (s *fakeSVNServer) port() int { return s.listener.Addr().(*net.TCPAddr).Port }

func (s *fakeSVNServer) serve() {
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), s.allowed.Marshal()) {
				return nil, errors.New("unknown key")
			}
			s.mu.Lock()
			s.user = meta.User()
			s.mu.Unlock()
			return nil, nil
		},
	}
	config.AddHostKey(s.hostKey)
	serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	running := make(chan struct{})
	go func() {
		for request := range requests {
			if request.Type == "keepalive@openssh.com" && s.silent {
				select {
				case <-running:
					continue
				default:
				}
			}
			_ = request.Reply(false, nil)
		}
	}()
	for newChannel := range channels {
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
			s.command = exec.Command
			s.mu.Unlock()
			_ = request.Reply(true, nil)
			close(running)
			if s.silent {
				select {}
			}
			line, _ := bufio.NewReader(channel).ReadString('\n')
			_, _ = io.WriteString(channel, "echo: "+line)
			status := make([]byte, 4)
			binary.BigEndian.PutUint32(status, s.exitCode)
			_, _ = channel.SendRequest("exit-status", false, status)
			_ = channel.Close()
			return
		}
	}
}

// identity writes an unencrypted OpenSSH ed25519 key, as FileES stores it.
func identity(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := ssh.NewPublicKey(public)
	return path, key
}

func knownHosts(t *testing.T, pattern string, key ssh.PublicKey) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(pattern+" "+string(ssh.MarshalAuthorizedKey(key))), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// openStdin never reaches EOF, like the pipe Subversion hands its tunnel.
func openStdin(line string) io.Reader {
	reader, writer := io.Pipe()
	go func() { _, _ = io.WriteString(writer, line) }()
	return reader
}

func run(t *testing.T, server *fakeSVNServer, opts Options, destination string) (int, string, string) {
	t.Helper()
	go server.serve()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, err := Run(ctx, opts, destination, "svnserve -t", openStdin("( 2 ( edit-pipeline ) )\n"), &stdout, &stderr)
	if err != nil {
		stderr.WriteString(err.Error())
	}
	return code, stdout.String(), stderr.String()
}

// The svn+ssh exchange: publickey with the one identity, the pinned host
// key, "svnserve -t" executed, streams bridged, the remote status returned -
// and Run must not wait for a stdin that never closes.
func TestExecBridgesStreamsAndReturnsTheRemoteStatus(t *testing.T) {
	key, public := identity(t)
	server := newFakeSVNServer(t, public)
	server.exitCode = 7
	opts := Options{Identity: key, KnownHosts: knownHosts(t, "[127.0.0.1]:"+strconv.Itoa(server.port()), server.hostKey.PublicKey()), Port: server.port()}
	code, stdout, stderr := run(t, server, opts, "_filees-data@127.0.0.1")
	if code != 7 || stdout != "echo: ( 2 ( edit-pipeline ) )\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.user != "_filees-data" || server.command != "svnserve -t" {
		t.Fatalf("user=%q command=%q", server.user, server.command)
	}
}

// The repository URL may name a host the client cannot reach (the server's
// url_prefix); HostName is where to connect and HostKeyAlias the name the key
// is pinned under, exactly as pkg/client passed them to OpenSSH.
func TestExecConnectsToHostNameAndVerifiesUnderTheAlias(t *testing.T) {
	key, public := identity(t)
	server := newFakeSVNServer(t, public)
	alias := "[svn.example.test]:" + strconv.Itoa(server.port())
	opts := Options{Identity: key, KnownHosts: knownHosts(t, alias, server.hostKey.PublicKey()), Port: server.port(), HostName: "127.0.0.1", HostKeyAlias: alias}
	if code, stdout, stderr := run(t, server, opts, "_filees-data@unreachable.invalid"); code != 0 || !strings.HasPrefix(stdout, "echo: ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// A key the server does not accept is reported in OpenSSH's words, which
// pkg/errmap maps to a refused identity rather than a network outage.
func TestExecReportsARefusedIdentityLikeOpenSSH(t *testing.T) {
	key, _ := identity(t)
	_, other := identity(t)
	server := newFakeSVNServer(t, other)
	opts := Options{Identity: key, KnownHosts: knownHosts(t, "[127.0.0.1]:"+strconv.Itoa(server.port()), server.hostKey.PublicKey()), Port: server.port()}
	code, _, stderr := run(t, server, opts, "_filees-data@127.0.0.1")
	if code != 255 || !strings.Contains(stderr, "Permission denied (publickey)") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// StrictHostKeyChecking=yes: a server whose key is not pinned is refused
// before the identity is offered.
func TestExecRefusesAnUnpinnedServer(t *testing.T) {
	key, public := identity(t)
	server := newFakeSVNServer(t, public)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewSignerFromKey(private)
	opts := Options{Identity: key, KnownHosts: knownHosts(t, "[127.0.0.1]:"+strconv.Itoa(server.port()), other.PublicKey()), Port: server.port()}
	code, _, stderr := run(t, server, opts, "_filees-data@127.0.0.1")
	if code != 255 || !strings.Contains(stderr, "Host key verification failed") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.user != "" {
		t.Fatal("the identity reached an unverified server")
	}
}

// ServerAliveCountMax: a server that stops answering is given up with the
// sentence pkg/errmap reads as a dropped connection.
func TestExecGivesUpASilentServer(t *testing.T) {
	key, public := identity(t)
	server := newFakeSVNServer(t, public)
	server.silent = true
	opts := Options{Identity: key, KnownHosts: knownHosts(t, "[127.0.0.1]:"+strconv.Itoa(server.port()), server.hostKey.PublicKey()), Port: server.port(),
		AliveInterval: 100 * time.Millisecond, AliveCountMax: 3}
	code, _, stderr := run(t, server, opts, "_filees-data@127.0.0.1")
	if code != 255 || !strings.Contains(stderr, "not responding") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// Main reads its arguments in the shape Subversion produces from SVN_SSH:
// the fixed flags, then [user@]host, then the remote command words.
func TestMainTakesTheArgumentsSubversionAppends(t *testing.T) {
	key, public := identity(t)
	server := newFakeSVNServer(t, public)
	go server.serve()
	kh := knownHosts(t, "[127.0.0.1]:"+strconv.Itoa(server.port()), server.hostKey.PublicKey())
	var stdout, stderr bytes.Buffer
	code := Main([]string{"-i", key, "-known-hosts", kh, "-alive", "15", "-alive-max", "3", "-p", strconv.Itoa(server.port()),
		"_filees-data@127.0.0.1", "svnserve", "-t"}, openStdin("hello\n"), &stdout, &stderr)
	if code != 0 || stdout.String() != "echo: hello\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.command != "svnserve -t" {
		t.Fatalf("command = %q", server.command)
	}
}
