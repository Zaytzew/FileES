package sshtransport

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	v1 "filees/pkg/mobile/v1"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// handlerFunc answers one framed request with a framed response, in-process —
// it stands in for the real sshd forced-command + mobile dispatcher so this
// test exercises Transport's SSH/framing plumbing without needing a real SVN
// repo or a system sshd.
type handlerFunc func(reqHeader, reqPayload []byte) (respHeader, respPayload []byte, exitOK bool)

func startFakeServer(t *testing.T, hostSigner ssh.Signer, clientKey ssh.PublicKey, handle handlerFunc) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !keysEqual(key, clientKey) {
				return nil, errors.New("fake server: unexpected client key")
			}
			return &ssh.Permissions{}, nil
		},
	}
	serverConfig.AddHostKey(hostSigner)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, chans, reqs, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for newChannel := range chans {
			if newChannel.ChannelType() != "session" {
				newChannel.Reject(ssh.UnknownChannelType, "only session channels")
				continue
			}
			channel, requests, err := newChannel.Accept()
			if err != nil {
				continue
			}
			go serveOneExec(channel, requests, handle)
		}
	}()
	return listener.Addr().String()
}

func serveOneExec(channel ssh.Channel, requests <-chan *ssh.Request, handle handlerFunc) {
	defer channel.Close()
	for req := range requests {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		req.Reply(true, nil)
		reqHeader, reqPayload, err := v1.ReadFrame(channel, v1.RequestMagic, v1.MaxHeaderBytes)
		status := uint32(0)
		if err != nil {
			status = 1
		} else {
			respHeader, respPayload, ok := handle(reqHeader, reqPayload)
			if !ok {
				status = 1
			} else if err := v1.WriteFrame(channel, v1.ResponseMagic, respHeader, respPayload); err != nil {
				status = 1
			}
		}
		channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

func generateEd25519(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return signer, sshPub
}

func TestDoRoundTripsRefreshManifest(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	clientSigner, clientPub := generateEd25519(t)

	var gotOperation v1.Operation
	addr := startFakeServer(t, hostSigner, clientPub, func(reqHeader, _ []byte) ([]byte, []byte, bool) {
		var req v1.Request
		if err := json.Unmarshal(reqHeader, &req); err != nil {
			return nil, nil, false
		}
		gotOperation = req.Operation
		resp, err := v1.NewSuccess(req.RequestID, req.Operation, v1.RefreshManifestResult{NotModified: true})
		if err != nil {
			return nil, nil, false
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			return nil, nil, false
		}
		return raw, nil, true
	})

	transport, err := New(Config{
		Address:       addr,
		User:          "filees-mobile-v1",
		HostPublicKey: string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())),
		Signer:        clientSigner,
		DialTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo-1"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, _, err := transport.Do(ctx, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotOperation != v1.OpRefreshManifest {
		t.Fatalf("server saw operation %q", gotOperation)
	}
	if resp.Status != v1.StatusOK {
		t.Fatalf("status = %v", resp.Status)
	}
	var result v1.RefreshManifestResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.NotModified {
		t.Fatalf("result = %+v", result)
	}
}

func TestDoRejectsWrongHostKey(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	wrongHostSigner, _ := generateEd25519(t)
	clientSigner, clientPub := generateEd25519(t)

	addr := startFakeServer(t, hostSigner, clientPub, func(_, _ []byte) ([]byte, []byte, bool) {
		return nil, nil, false
	})

	transport, err := New(Config{
		Address:       addr,
		User:          "filees-mobile-v1",
		HostPublicKey: string(ssh.MarshalAuthorizedKey(wrongHostSigner.PublicKey())),
		Signer:        clientSigner,
		DialTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := transport.Do(context.Background(), req, nil); err == nil {
		t.Fatal("expected host key mismatch to be rejected")
	}
}

func TestNewRejectsMissingFields(t *testing.T) {
	signer, _ := generateEd25519(t)
	hostSigner, _ := generateEd25519(t)
	validHostKey := string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))

	cases := []Config{
		{User: "u", HostPublicKey: validHostKey, Signer: signer},              // missing address
		{Address: "127.0.0.1:1", HostPublicKey: validHostKey, Signer: signer}, // missing user
		{Address: "127.0.0.1:1", User: "u", Signer: signer},                   // missing host key
		{Address: "127.0.0.1:1", User: "u", HostPublicKey: validHostKey},      // missing signer
	}
	for i, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}
