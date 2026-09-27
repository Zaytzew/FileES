package androidbind

import (
	"bytes"
	"context"
	"encoding/json"
	"filees/internal/mobileworker"
	v1 "filees/pkg/mobile/v1"
	"golang.org/x/crypto/ssh"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Explicit opt-in bridge for the instrumented Android APK. Binds loopback,
// pins the first emulator key, uses a disposable SVN repo and drops exactly
// the first committed tree ACK plus its status response. No deployed server.
func TestCaptureEmulatorBackend(t *testing.T) {
	root := os.Getenv("FILEES_CAPTURE_EMULATOR_FIXTURE")
	if root == "" {
		t.Skip("requires instrumented emulator and explicit fixture directory")
	}
	requireSVN(t)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	repo := newSeededRepo(t)
	d := newDispatcher(repo)
	d.Appender.Ledger = mobileworker.Ledger{Dir: t.TempDir()}
	host := generateEd25519(t)
	var mu sync.Mutex
	var pinned string
	droppedUpload, droppedStatus := false, false
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		mu.Lock()
		defer mu.Unlock()
		encoded := string(key.Marshal())
		if pinned == "" {
			pinned = encoded
		}
		if pinned != encoded {
			return nil, errUnauthorizedKey
		}
		return &ssh.Permissions{}, nil
	}}
	cfg.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:22380")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, _ := json.Marshal(map[string]string{"host_key": string(ssh.MarshalAuthorizedKey(host.PublicKey()))})
	if err := os.WriteFile(filepath.Join(root, "endpoint.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, channels, requests, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								request.Reply(false, nil)
								continue
							}
							request.Reply(true, nil)
							header, body, err := v1.ReadFrame(channel, v1.RequestMagic, v1.MaxHeaderBytes)
							if err != nil {
								return
							}
							req, err := v1.ParseRequest(header)
							if err != nil {
								return
							}
							mu.Lock()
							dropStatus := req.Operation == v1.OpOperationStatus && droppedUpload && !droppedStatus
							if dropStatus {
								droppedStatus = true
							}
							mu.Unlock()
							if dropStatus {
								return
							}
							var frame, response bytes.Buffer
							_ = v1.WriteFrame(&frame, v1.RequestMagic, header, body)
							if err := d.Serve(context.Background(), &frame, &response); err != nil {
								return
							}
							mu.Lock()
							dropUpload := req.Operation == v1.OpUploadTree && !droppedUpload
							if dropUpload {
								droppedUpload = true
							}
							mu.Unlock()
							if dropUpload {
								return
							}
							_, _ = channel.Write(response.Bytes())
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					}()
				}
			}()
		}
	}()
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("emulator fixture timed out")
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(root, "done")); err == nil {
				mu.Lock()
				both := droppedUpload && droppedStatus
				mu.Unlock()
				if !both {
					t.Fatal("fault injection not exercised")
				}
				return
			}
		}
	}
}
