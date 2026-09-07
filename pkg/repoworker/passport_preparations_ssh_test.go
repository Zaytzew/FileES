package repoworker

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func preparationSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer, path
}

func passportSSHFixture(t *testing.T, dispatcher Dispatcher, clientID string, dropReply func(int32) bool) (*controlclient.Client, controlclient.Config, *atomic.Int32) {
	t.Helper()
	host, _ := preparationSigner(t)
	identity, identityPath := preparationSigner(t)
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != controlclient.ServiceUser || !bytes.Equal(key.Marshal(), identity.PublicKey().Marshal()) {
			return nil, fmt.Errorf("unknown fixture identity")
		}
		return &ssh.Permissions{Extensions: map[string]string{"client_id": clientID}}, nil
	}}
	serverConfig.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pins := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(pins, []byte(knownhosts.Line([]string{listener.Addr().String()}, host.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var handled atomic.Int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for next := range channels {
					if next.ChannelType() != "session" {
						_ = next.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, reqs, err := next.Accept()
					if err != nil {
						return
					}
					for req := range reqs {
						var command struct{ Command string }
						if req.Type != "exec" || ssh.Unmarshal(req.Payload, &command) != nil || command.Command != controlclient.Command {
							_ = req.Reply(false, nil)
							continue
						}
						_ = req.Reply(true, nil)
						var out bytes.Buffer
						err := dispatcher.Serve(t.Context(), server.Permissions.Extensions["client_id"], channel, &out)
						n := handled.Add(1)
						if dropReply(n) {
							// Disconnect only after the worker has returned its durable result.
							_ = server.Close()
							return
						}
						code := uint32(0)
						if err != nil {
							code = 1
							_, _ = io.WriteString(channel.Stderr(), err.Error())
						} else {
							_, _ = channel.Write(out.Bytes())
						}
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
						_ = channel.Close()
						break
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); wg.Wait() })
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	cfg := controlclient.Config{Address: "127.0.0.1", Port: port, IdentityFile: identityPath, KnownHosts: pins, Timeout: 5 * time.Second}
	client, err := controlclient.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client, cfg, &handled
}

func TestPassportPreparationPinnedSSHAndLostReply(t *testing.T) {
	f, _, doc := realReplacementFixture(t)
	ticket := preparationTicket(t, f)
	svc := &PassportPreparations{Root: t.TempDir(), Authority: f.authority}
	dispatcher := Dispatcher{Worker: &Worker{PassportPreparations: svc}, Resolver: preparationResolver{f.session}, Admission: preparationAdmission{}}
	client, cfg, handled := passportSSHFixture(t, dispatcher, f.session.ClientID, func(n int32) bool { return n == 1 || n == 4 })
	if err := controlclient.PreparePassportReplacement(t.Context(), client, ticket); err == nil {
		t.Fatal("lost SSH reply acknowledged")
	}
	lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || lock != nil {
		t.Fatalf("prepare not executed before disconnect: %+v %v", lock, err)
	}
	replacementCommand(t, "svn", "lock", "--username", f.session.ClientID, "-m", "new lock after reconnect", doc)
	before, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || before == nil {
		t.Fatal(err)
	}
	if err := controlclient.PreparePassportReplacement(t.Context(), client, ticket); err != nil {
		t.Fatal(err)
	}
	after, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || after == nil || after.Token != before.Token {
		t.Fatalf("replayed unlock: %+v %v", after, err)
	}
	wrong := ticket
	wrong.ClientID = f.guest
	if err := controlclient.PreparePassportReplacement(t.Context(), client, wrong); err == nil {
		t.Fatal("payload client replaced SSH identity")
	}
	if err := controlclient.CancelPassportPreparation(t.Context(), client, ticket); err == nil {
		t.Fatal("lost cancellation reply acknowledged")
	}
	if err := controlclient.CancelPassportPreparation(t.Context(), client, ticket); err != nil {
		t.Fatal(err)
	}
	if err := controlclient.PreparePassportReplacement(t.Context(), client, ticket); !controlclient.IsAbortedPreparation(err, ticket) {
		t.Fatalf("late prepare: %v", err)
	}
	if err := controlclient.CancelPassportPreparation(t.Context(), client, wrong); err == nil {
		t.Fatal("cancellation replaced SSH identity")
	}
	after, err = f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || after == nil || after.Token != before.Token {
		t.Fatalf("cancellation changed token: %+v %v", after, err)
	}
	badHost, _ := preparationSigner(t)
	badPins := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(badPins, []byte(knownhosts.Line([]string{net.JoinHostPort(cfg.Address, strconv.Itoa(cfg.Port))}, badHost.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.KnownHosts = badPins
	badClient, err := controlclient.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badClient.Exchange(t.Context(), ticket); err == nil {
		t.Fatal("wrong host pin accepted")
	}
	if handled.Load() != 7 {
		t.Fatalf("unexpected dispatch count %d", handled.Load())
	}
}

func TestPassportExecutionPinnedSSHAndLostReplies(t *testing.T) {
	f := newExecutionFixture(t)
	dispatcher := Dispatcher{Worker: &Worker{PassportExecutions: &f.svc}, Resolver: preparationResolver{f.session}, Admission: preparationAdmission{}}
	client, _, handled := passportSSHFixture(t, dispatcher, f.session.ClientID, func(n int32) bool { return n == 1 || n == 4 })
	if err := controlclient.PassportExecution(t.Context(), client, f.arm); err == nil {
		t.Fatal("lost ARM reply acknowledged")
	}
	if f.record(t).State != "armed" {
		t.Fatal("disconnect preceded durable ARM")
	}
	if err := controlclient.PassportExecution(t.Context(), client, f.arm); err != nil {
		t.Fatal(err)
	}
	f.lock(t)
	settle := f.arm
	settle.Type = control.TicketSettlePassportAcquisition
	wrong := settle
	wrong.ClientID = f.guest
	if err := controlclient.PassportExecution(t.Context(), client, wrong); err == nil {
		t.Fatal("payload actor replaced authenticated SSH identity")
	}
	if f.record(t).State != "started" {
		t.Fatal("wrong actor changed execution")
	}
	if err := controlclient.PassportExecution(t.Context(), client, settle); err == nil {
		t.Fatal("lost SETTLE reply acknowledged")
	}
	if f.record(t).State != "closed" {
		t.Fatal("disconnect preceded durable SETTLE")
	}
	if lock, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path); err != nil || lock != nil {
		t.Fatalf("SETTLE did not release original token: %+v %v", lock, err)
	}
	replacementCommand(t, "svn", "lock", "--username", f.session.ClientID, "-m", "new token after SSH disconnect", f.doc)
	before, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || before == nil {
		t.Fatalf("missing successor: %+v %v", before, err)
	}
	if err := controlclient.PassportExecution(t.Context(), client, settle); err != nil {
		t.Fatal(err)
	}
	if err := controlclient.PassportExecution(t.Context(), client, f.arm); err == nil {
		t.Fatal("closed ARM reopened on reconnect")
	}
	after, err := f.authority.Locks.inspectSVNLock(t.Context(), f.req.RepoID, f.req.Path)
	if err != nil || after == nil || after.Token != before.Token {
		t.Fatalf("reconnect changed successor token: %+v %v", after, err)
	}
	if handled.Load() != 6 {
		t.Fatalf("unexpected dispatch count %d", handled.Load())
	}
	if data, err := os.ReadFile(f.doc); err != nil || string(data) != "local work" {
		t.Fatalf("changed working bytes: %q %v", data, err)
	}
}
