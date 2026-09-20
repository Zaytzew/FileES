package controlclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	control "filees/pkg/control/v1"
	"fmt"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestIdentityRefusalRequiresVerifiedHostAndAuthenticationRejection(t *testing.T) {
	for _, mode := range []string{"refused", "wrong-pin", "disconnected"} {
		t.Run(mode, func(t *testing.T) {
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			block, err := ssh.MarshalPrivateKey(private, "test-only")
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.NewSignerFromKey(private)
			if err != nil {
				t.Fatal(err)
			}
			_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			host, err := ssh.NewSignerFromKey(hostPrivate)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				if mode == "disconnected" {
					return
				}
				cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
					return nil, fmt.Errorf("test revoked credential")
				}}
				cfg.AddHostKey(host)
				if server, _, _, err := ssh.NewServerConn(conn, cfg); err == nil {
					server.Close()
				}
			}()
			root := t.TempDir()
			identity, pins := filepath.Join(root, "identity"), filepath.Join(root, "known_hosts")
			if err := os.WriteFile(identity, pem.EncodeToMemory(block), 0600); err != nil {
				t.Fatal(err)
			}
			pin := host.PublicKey()
			if mode == "wrong-pin" {
				pin = signer.PublicKey()
			}
			if err := os.WriteFile(pins, []byte(knownhosts.Line([]string{listener.Addr().String()}, pin)+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			address, portText, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				t.Fatal(err)
			}
			client, err := New(Config{Address: address, Port: port, IdentityFile: identity, KnownHosts: pins, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketClientDeactivate, uuid.NewString(), control.ClientDeactivatePayload{}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Exchange(t.Context(), ticket)
			if err == nil || errors.Is(err, ErrIdentityRefused) != (mode == "refused") {
				t.Fatalf("%s classification: %v", mode, err)
			}
			<-done
		})
	}
}
