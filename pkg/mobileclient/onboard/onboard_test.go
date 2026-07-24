package onboard

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeServer stands in for the real sshd + forced-command chain
// (internal/servertool/entry.go / mobile_entry.go / mobile_worker.go), just
// enough to exercise this package's SSH/exec plumbing without a real sshd,
// a real onboarding store or a real activation.Manager. Each test dials
// sequentially, but connections are handled on their own goroutines, so
// sawCommands is guarded explicitly rather than relying on the network
// round-trip to imply a happens-before relationship.
type fakeServer struct {
	hostSigner ssh.Signer
	wantToken  string
	clientKey  ssh.PublicKey
	handleExec func(command string, stdin []byte) (workerResult, bool)

	mu          sync.Mutex
	sawCommands []string
}

func (f *fakeServer) recordCommand(command string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawCommands = append(f.sawCommands, command)
}

func (f *fakeServer) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sawCommands...)
}

func startFakeServer(t *testing.T, f *fakeServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if f.clientKey == nil || key.Type() != f.clientKey.Type() || string(key.Marshal()) != string(f.clientKey.Marshal()) {
				return nil, errors.New("fake server: unexpected client key")
			}
			return &ssh.Permissions{}, nil
		},
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := client("", "", []string{"response"}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) != 1 || answers[0] != f.wantToken {
				return nil, errors.New("fake server: wrong token")
			}
			return &ssh.Permissions{}, nil
		},
	}
	serverConfig.AddHostKey(f.hostSigner)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
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
					go f.serveOneExec(channel, requests)
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func (f *fakeServer) serveOneExec(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		ssh.Unmarshal(req.Payload, &payload)
		req.Reply(true, nil)
		f.recordCommand(payload.Command)

		stdin, _ := io.ReadAll(channel)
		status := uint32(0)
		if f.handleExec == nil {
			status = 1
		} else if result, ok := f.handleExec(payload.Command, stdin); !ok {
			status = 1
		} else if err := json.NewEncoder(channel).Encode(result); err != nil {
			status = 1
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

func TestPushInstallationKeyRoundTrips(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	f := &fakeServer{hostSigner: hostSigner, wantToken: "ABCDEFGH-IJKLMNOPQRSTUVWX"}
	var gotPayload pushPayload
	f.handleExec = func(command string, stdin []byte) (workerResult, bool) {
		if command != pushCommand {
			return workerResult{}, false
		}
		if err := json.Unmarshal(stdin, &gotPayload); err != nil {
			return workerResult{}, false
		}
		return workerResult{Schema: "filees.mobile-worker-result/v1", Status: "staged", OperationID: "op-1", ClientID: "client-1"}, true
	}
	addr := startFakeServer(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	operationID, clientID, err := PushInstallationKey(ctx, PairingConfig{
		Address:       addr,
		HostPublicKey: string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())),
		DialTimeout:   2 * time.Second,
	}, f.wantToken, "ssh-ed25519 AAAA filees:mobile-1", "SHA256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if operationID != "op-1" || clientID != "client-1" {
		t.Fatalf("operationID=%q clientID=%q", operationID, clientID)
	}
	if gotPayload.PublicKey != "ssh-ed25519 AAAA filees:mobile-1" || gotPayload.Fingerprint != "SHA256:abc" {
		t.Fatalf("server saw payload=%+v", gotPayload)
	}
}

func TestPushInstallationKeyRejectsWrongToken(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	f := &fakeServer{hostSigner: hostSigner, wantToken: "ABCDEFGH-IJKLMNOPQRSTUVWX"}
	addr := startFakeServer(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := PushInstallationKey(ctx, PairingConfig{
		Address:       addr,
		HostPublicKey: string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())),
		DialTimeout:   2 * time.Second,
	}, "WRONGTOK-ENXXXXXXXXXXXXXXX", "ssh-ed25519 AAAA filees:mobile-1", "SHA256:abc")
	if err == nil {
		t.Fatal("wrong token accepted")
	}
}

func TestPushInstallationKeyRejectsWrongHostKey(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	wrongHostSigner, _ := generateEd25519(t)
	f := &fakeServer{hostSigner: hostSigner, wantToken: "ABCDEFGH-IJKLMNOPQRSTUVWX"}
	addr := startFakeServer(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := PushInstallationKey(ctx, PairingConfig{
		Address:       addr,
		HostPublicKey: string(ssh.MarshalAuthorizedKey(wrongHostSigner.PublicKey())),
		DialTimeout:   2 * time.Second,
	}, f.wantToken, "ssh-ed25519 AAAA filees:mobile-1", "SHA256:abc")
	if err == nil {
		t.Fatal("wrong host key accepted")
	}
}

func TestProveAndFinishRoundTrip(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	clientSigner, clientPub := generateEd25519(t)
	f := &fakeServer{hostSigner: hostSigner, clientKey: clientPub}
	f.handleExec = func(command string, _ []byte) (workerResult, bool) {
		switch command {
		case proofCommand:
			return workerResult{Status: "proved", OperationID: "op-1", ClientID: "client-1"}, true
		case finishCommand:
			return workerResult{Status: "active", OperationID: "op-1", ClientID: "client-1", ServiceRevision: 7}, true
		default:
			return workerResult{}, false
		}
	}
	addr := startFakeServer(t, f)

	cfg := ProofConfig{
		Address:       addr,
		User:          "device-1",
		HostPublicKey: string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())),
		Signer:        clientSigner,
		DialTimeout:   2 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	operationID, clientID, err := ProveInstallationKey(ctx, cfg)
	if err != nil || operationID != "op-1" || clientID != "client-1" {
		t.Fatalf("prove: operationID=%q clientID=%q err=%v", operationID, clientID, err)
	}

	operationID, clientID, revision, err := FinishActivation(ctx, cfg)
	if err != nil || operationID != "op-1" || clientID != "client-1" || revision != 7 {
		t.Fatalf("finish: operationID=%q clientID=%q revision=%d err=%v", operationID, clientID, revision, err)
	}
	if got := f.commands(); len(got) != 2 || got[0] != proofCommand || got[1] != finishCommand {
		t.Fatalf("server saw commands=%v", got)
	}
}

func TestProveInstallationKeyRejectsWrongKey(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	_, clientPub := generateEd25519(t)
	otherSigner, _ := generateEd25519(t)
	f := &fakeServer{hostSigner: hostSigner, clientKey: clientPub}
	addr := startFakeServer(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := ProveInstallationKey(ctx, ProofConfig{
		Address:       addr,
		User:          "device-1",
		HostPublicKey: string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())),
		Signer:        otherSigner,
		DialTimeout:   2 * time.Second,
	})
	if err == nil {
		t.Fatal("wrong installation key accepted")
	}
}

func TestPairingConfigRejectsMissingFields(t *testing.T) {
	if _, _, err := (PairingConfig{}).validate(); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, _, err := (PairingConfig{Address: "127.0.0.1:1"}).validate(); err == nil {
		t.Fatal("missing host key accepted")
	}
}

func TestProofConfigRejectsMissingFields(t *testing.T) {
	hostSigner, _ := generateEd25519(t)
	hostKeyLine := string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))
	if _, _, err := (ProofConfig{Address: "127.0.0.1:1", HostPublicKey: hostKeyLine}).validate(); err == nil {
		t.Fatal("missing user/signer accepted")
	}
}

// TestIsKeyUnauthorizedRecognizesBothRealSSHRejectionForms is the E2E-found
// regression guard: golang.org/x/crypto/ssh's own "unable to authenticate"
// (the client exhausting its configured methods) is only ONE real rejection
// shape. Against a real OpenBSD sshd with a low MaxAuthTries - confirmed
// live against the operational _filees-mobile class (MaxAuthTries 1) - the
// SERVER disconnects first with "Too many authentication failures" instead,
// which an in-process fake SSH server's default leniency never reproduces.
// Both must be recognized as "key not yet staged", not treated as an
// unexpected fatal error.
func TestIsKeyUnauthorizedRecognizesBothRealSSHRejectionForms(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"client exhausted methods", errors.New(`onboard: handshake: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain`), true},
		{"server MaxAuthTries disconnect", errors.New(`onboard: handshake: ssh: handshake failed: ssh: disconnect, reason 2: "Too many authentication failures"`), true},
		{"unrelated network error", errors.New(`onboard: dial: dial tcp 10.0.2.2:2222: connect: connection refused`), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsKeyUnauthorized(c.err); got != c.want {
				t.Fatalf("IsKeyUnauthorized(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
