package reservationclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	reservationv1 "filees/pkg/reservation/v1"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const testRepoID = "f5d5bfee-62f4-5b9c-b26f-8d4c424fb8f0"

func TestFetchParsesAFreshResult(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	asOf := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	address := startReservationSSH(t, hostSigner, clientSigner.PublicKey(), func(stdin *bufio.Reader, stdout *bytes.Buffer) {
		var req reservationv1.Request
		line, _ := stdin.ReadString('\n')
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &req); err != nil || req.RepoID != testRepoID {
			return
		}
		result := reservationv1.Result{
			Schema: reservationv1.Schema, RepoID: req.RepoID,
			Reservations: []reservationv1.Reservation{{Path: "a.txt", Token: "tok", OwnerID: "acme"}},
			AsOf:         asOf, Generation: "3",
		}
		raw, _ := json.Marshal(result)
		stdout.Write(append(raw, '\n'))
	})

	client := configuredClient(t, address, hostSigner.PublicKey(), clientPrivate)
	result, err := client.Fetch(context.Background(), testRepoID)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result.Stale || result.Unknown || len(result.Reservations) != 1 || result.Generation != "3" || !result.AsOf.Equal(asOf) {
		t.Fatalf("result=%+v", result)
	}
}

func TestFetchRejectsWorkerFailure(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	address := startReservationSSHWithExit(t, hostSigner, clientSigner.PublicKey(), func(*bufio.Reader, *bytes.Buffer) {}, 70)
	client := configuredClient(t, address, hostSigner.PublicKey(), clientPrivate)
	if _, err := client.Fetch(context.Background(), testRepoID); err == nil {
		t.Fatal("expected a non-zero worker exit to surface as an error")
	}
}

func TestFetchRejectsMismatchedRepoID(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	address := startReservationSSH(t, hostSigner, clientSigner.PublicKey(), func(_ *bufio.Reader, stdout *bytes.Buffer) {
		result := reservationv1.Result{Schema: reservationv1.Schema, RepoID: "11111111-1111-4111-8111-111111111111", AsOf: time.Now(), Generation: "1"}
		raw, _ := json.Marshal(result)
		stdout.Write(append(raw, '\n'))
	})
	client := configuredClient(t, address, hostSigner.PublicKey(), clientPrivate)
	if _, err := client.Fetch(context.Background(), testRepoID); err == nil {
		t.Fatal("expected a result for a different repo id to be rejected")
	}
}

func TestFetchRejectsOversizedResponse(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	address := startReservationSSH(t, hostSigner, clientSigner.PublicKey(), func(_ *bufio.Reader, stdout *bytes.Buffer) {
		// Larger than MaxResponseBytes; the raw bytes need not be valid
		// JSON, since the size check must reject this before parsing.
		stdout.Write(bytes.Repeat([]byte("x"), MaxResponseBytes+1))
	})
	client := configuredClient(t, address, hostSigner.PublicKey(), clientPrivate)
	if _, err := client.Fetch(context.Background(), testRepoID); err == nil {
		t.Fatal("expected an oversized response to be rejected")
	}
}

func TestFetchRejectsTrailingDataAfterResult(t *testing.T) {
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	address := startReservationSSH(t, hostSigner, clientSigner.PublicKey(), func(_ *bufio.Reader, stdout *bytes.Buffer) {
		result := reservationv1.Result{Schema: reservationv1.Schema, RepoID: testRepoID, AsOf: time.Now(), Generation: "1"}
		raw, _ := json.Marshal(result)
		stdout.Write(append(raw, []byte(`{"schema":"evil"}`)...))
	})
	client := configuredClient(t, address, hostSigner.PublicKey(), clientPrivate)
	if _, err := client.Fetch(context.Background(), testRepoID); err == nil {
		t.Fatal("expected a second smuggled JSON value after the result to be rejected")
	}
}

func configuredClient(t *testing.T, address string, hostKey ssh.PublicKey, private ed25519.PrivateKey) *Client {
	t.Helper()
	root := t.TempDir()
	keyBlock, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(root, "id_ed25519")
	if err := os.WriteFile(identity, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(root, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{address}, hostKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(address)
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{Address: host, Port: port, IdentityFile: identity, KnownHosts: known, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func startReservationSSH(t *testing.T, hostSigner ssh.Signer, clientKey ssh.PublicKey, handle func(*bufio.Reader, *bytes.Buffer)) string {
	t.Helper()
	return startReservationSSHWithExit(t, hostSigner, clientKey, handle, 0)
}

func startReservationSSHWithExit(t *testing.T, hostSigner ssh.Signer, clientKey ssh.PublicKey, handle func(*bufio.Reader, *bytes.Buffer), exitCode uint32) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !publicKeysEqual(key, clientKey) {
			return nil, errors.New("wrong client key")
		}
		return &ssh.Permissions{}, nil
	}}
	serverConfig.AddHostKey(hostSigner)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for next := range channels {
			if next.ChannelType() != "session" {
				next.Reject(ssh.UnknownChannelType, "session required")
				continue
			}
			channel, requests, err := next.Accept()
			if err != nil {
				return
			}
			go func() {
				defer channel.Close()
				var stdout bytes.Buffer
				for request := range requests {
					if request.Type != "exec" {
						request.Reply(false, nil)
						continue
					}
					request.Reply(true, nil)
					handle(bufio.NewReader(channel), &stdout)
					channel.Write(stdout.Bytes())
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitCode}))
					return
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func generateKey(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, private
}

func publicKeysEqual(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && string(a.Marshal()) == string(b.Marshal())
}
