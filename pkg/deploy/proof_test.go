package deploy

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestSSHAccessProverUsesGeneratedIdentityAndPinnedServiceHost(t *testing.T) {
	operationID, clientID := newUUID(), newUUID()
	identityRoot := filepath.Join(t.TempDir(), "identity")
	identity, err := (IdentityGenerator{Root: identityRoot}).GenerateInstallationIdentity(operationID, clientID)
	if err != nil {
		t.Fatal(err)
	}
	installationKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(identity.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	address, hostKey, observed, closeServer := startProofSSHServer(t, installationKey)
	defer closeServer()
	prover := SSHAccessProver{Address: address, HostPublicKey: hostKey, IdentityRoot: identityRoot, Timeout: time.Second}
	if err := prover.ProveServiceAccess(operationID, clientID); err != nil {
		t.Fatal(err)
	}
	select {
	case command := <-observed:
		if command != ServiceProofCommand {
			t.Fatalf("proof command=%q", command)
		}
	case <-time.After(time.Second):
		t.Fatal("service endpoint did not observe proof")
	}
	wrongHost := newTestSigner(t)
	prover.HostPublicKey = string(ssh.MarshalAuthorizedKey(wrongHost.PublicKey()))
	if err := prover.ProveServiceAccess(operationID, clientID); err == nil {
		t.Fatal("service proof accepted a different host key")
	}
}

func startProofSSHServer(t *testing.T, installationKey ssh.PublicKey) (string, string, <-chan string, func()) {
	t.Helper()
	host := newTestSigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	observed := make(chan string, 2)
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go serveProofSSHConnection(ctx, raw, host, installationKey, observed)
		}
	}()
	return listener.Addr().String(), string(ssh.MarshalAuthorizedKey(host.PublicKey())), observed, func() {
		cancel()
		_ = listener.Close()
	}
}

func serveProofSSHConnection(ctx context.Context, raw net.Conn, host ssh.Signer, installationKey ssh.PublicKey, observed chan<- string) {
	defer raw.Close()
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != ServiceClientUser || !keysEqual(key, installationKey) {
			return nil, errors.New("proof key rejected")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	connection, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		return
	}
	defer connection.Close()
	go ssh.DiscardRequests(requests)
	for {
		select {
		case <-ctx.Done():
			return
		case channel, ok := <-channels:
			if !ok {
				return
			}
			if channel.ChannelType() != "session" {
				_ = channel.Reject(ssh.Prohibited, "session only")
				continue
			}
			stream, reqs, err := channel.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer stream.Close()
				for request := range reqs {
					command, ok := parseExecPayload(request.Payload)
					if request.Type != "exec" || !ok || command != ServiceProofCommand {
						_ = request.Reply(false, nil)
						continue
					}
					_ = request.Reply(true, nil)
					observed <- command
					status := make([]byte, 4)
					binary.BigEndian.PutUint32(status, 0)
					_, _ = stream.SendRequest("exit-status", false, status)
					return
				}
			}()
		}
	}
}

func newUUID() string {
	return uuid.NewString()
}
