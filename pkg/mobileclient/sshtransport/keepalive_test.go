package sshtransport

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	v1 "filees/pkg/mobile/v1"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// Each half expires on idle, as a TCP relay can while the worker commits.
func idleRelay(t *testing.T, target string, idle time.Duration) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		incoming, err := l.Accept()
		if err != nil {
			return
		}
		defer incoming.Close()
		outgoing, err := net.Dial("tcp", target)
		if err != nil {
			return
		}
		defer outgoing.Close()
		pump := func(dst, src net.Conn) {
			defer dst.Close()
			defer src.Close()
			b := make([]byte, 4096)
			for {
				src.SetReadDeadline(time.Now().Add(idle))
				n, err := src.Read(b)
				if n > 0 {
					if _, e := dst.Write(b[:n]); e != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}
		done := make(chan struct{})
		go func() { defer close(done); pump(outgoing, incoming) }()
		pump(incoming, outgoing)
		<-done
	}()
	return l.Addr().String()
}

func TestSlowResponseThroughIdleRelay(t *testing.T) {
	for _, keepalive := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_keepalive_expires", true: "with_keepalive_receipt_arrives"}[keepalive], func(t *testing.T) {
			host, _ := generateEd25519(t)
			signer, key := generateEd25519(t)
			addr := startFakeServer(t, host, key, func(header, _ []byte) ([]byte, []byte, bool) {
				time.Sleep(900 * time.Millisecond)
				var req v1.Request
				if json.Unmarshal(header, &req) != nil {
					return nil, nil, false
				}
				resp, _ := v1.NewSuccess(req.RequestID, req.Operation, v1.RefreshManifestResult{NotModified: true})
				raw, _ := json.Marshal(resp)
				return raw, nil, true
			})
			transport, err := New(Config{Address: idleRelay(t, addr, 300*time.Millisecond), User: "mobile", HostPublicKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), Signer: signer})
			if err != nil {
				t.Fatal(err)
			}
			// Test intervals are shorter; production defaults are 30s/15s.
			transport.keepaliveInterval = 60 * time.Millisecond
			transport.keepaliveTimeout = 200 * time.Millisecond
			if !keepalive {
				transport.keepaliveInterval = time.Hour
			}
			req, _ := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo"})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			resp, _, err := transport.Do(ctx, req, nil)
			if keepalive {
				if err != nil || resp.Status != v1.StatusOK {
					t.Fatalf("receipt: %v, %v", resp, err)
				}
			} else if err == nil {
				t.Fatal("idle connection unexpectedly survived")
			}
		})
	}
}

// A peer accepting TCP/SSH but never answering requests must not keep a
// mobile upload stuck until the ten-minute operation deadline.
func TestKeepaliveUnresponsivePeerAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "probe_timeout", true: "caller_cancel"}[cancelEarly], func(t *testing.T) {
			host, _ := generateEd25519(t)
			signer, _ := generateEd25519(t)
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				conn, e := l.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				cfg := &ssh.ServerConfig{NoClientAuth: true}
				cfg.AddHostKey(host)
				s, channels, requests, e := ssh.NewServerConn(conn, cfg)
				if e != nil {
					return
				}
				defer s.Close()
				go func() {
					for range requests { /* simulate lost keepalive replies */
					}
				}()
				for ch := range channels {
					c, rs, e := ch.Accept()
					if e != nil {
						return
					}
					go func() {
						defer c.Close()
						for r := range rs {
							r.Reply(true, nil)
							if r.Type == "exec" {
								io.Copy(io.Discard, c)
								// EOF on stdin does not close the session: the
								// stalled worker has not returned a response.
							}
						}
					}()
				}
			}()
			transport, err := New(Config{Address: l.Addr().String(), User: "mobile", HostPublicKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), Signer: signer})
			if err != nil {
				t.Fatal(err)
			}
			transport.keepaliveInterval = 50 * time.Millisecond
			transport.keepaliveTimeout = 200 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelEarly {
				time.AfterFunc(100*time.Millisecond, cancel)
			}
			req, _ := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo"})
			finished := make(chan error, 1)
			started := time.Now()
			go func() { _, _, err := transport.Do(ctx, req, nil); finished <- err }()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("missing transport error")
				}
				if !cancelEarly && time.Since(started) < 200*time.Millisecond {
					t.Fatal("session ended before probe timeout")
				}
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("stuck on unresponsive SSH peer")
			}
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("SSH connection leaked")
			}
		})
	}
}

func TestHandshakeDeadlineWithoutCallerDeadline(t *testing.T) {
	host, _ := generateEd25519(t)
	signer, _ := generateEd25519(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(io.Discard, conn)
	}()
	transport, err := New(Config{Address: l.Addr().String(), User: "mobile", HostPublicKey: string(ssh.MarshalAuthorizedKey(host.PublicKey())), Signer: signer, DialTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := v1.NewRequest(uuid.NewString(), v1.OpRefreshManifest, v1.RefreshManifestPayload{RepoID: "repo"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := transport.Do(ctx, req, nil); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing handshake timeout")
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("handshake ignored dial timeout")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("handshake connection leaked")
	}
}
