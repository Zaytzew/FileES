package whaleclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/internal/whaleworker"
	"filees/pkg/clientprofile"
	"filees/pkg/repoworker"
	whale "filees/pkg/whale/v1"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type releaseCLISource struct {
	identity whale.Identity
	content  []byte
}

func (s releaseCLISource) Discover(context.Context, string, string, string, int64) (whale.Identity, int64, error) {
	return s.identity, 7, nil
}
func (s releaseCLISource) Quote(context.Context, string, whale.Identity, int64) error { return nil }
func (s releaseCLISource) Materialize(_ context.Context, _ string, _ whale.Identity, _ int64, dest string) error {
	return os.WriteFile(dest, s.content, 0600)
}

type releaseCLIAuthority struct{ root string }

func (a releaseCLIAuthority) ResolveWhale(context.Context, string, string) (whaleworker.RepositoryAccess, error) {
	return whaleworker.RepositoryAccess{RepositoryPath: a.root, Access: "r"}, nil
}

// Runs the actual one-shot CLI against a pinned local SSH endpoint and real
// GetService/FileReservationLedger. Only repository discovery/materialization
// and authorization are fixtures; cache persistence and release are production
// code. Opt-in also lets the exact cross-built release run on OpenBSD.
func TestCLIGetReleaseReceipt(t *testing.T) {
	binary := os.Getenv("FILEES_CLI_E2E_BINARY")
	if binary == "" {
		t.Skip("set FILEES_CLI_E2E_BINARY to the actual CLI executable")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("CLI executable must be absolute")
	}
	for _, mode := range []string{"delayed", "lost", "killed"} {
		t.Run(mode, func(t *testing.T) { testCLIGetReleaseReceipt(t, binary, mode) })
	}
}

func testCLIGetReleaseReceipt(t *testing.T, binary, mode string) {
	root := t.TempDir()
	cache, ledger := filepath.Join(root, "cache"), filepath.Join(root, "ledger")
	content := bytes.Repeat([]byte("immutable fixture\n"), 4096)
	digest := sha256.Sum256(content)
	identity := whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/file.bin", GenerationID: uuid.NewString(), ExpectedSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	service := whaleworker.GetService{Root: cache, Authority: releaseCLIAuthority{root}, Source: releaseCLISource{identity, content}, Reservations: &repoworker.FileReservationLedger{Root: ledger, Capacity: repoworker.FilesystemCapacity{Root: root}}}
	started, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(unblock) })
	var mu sync.Mutex
	var releases []whale.Request
	windows := 0
	hostSigner, _ := generateKey(t)
	clientSigner, clientPrivate := generateKey(t)
	clientID := uuid.NewString()
	address := releaseCLIEndpoint(t, hostSigner, clientSigner.PublicKey(), func(channel ssh.Channel) {
		header, err := whale.ReadHeader(bufio.NewReader(channel), whale.RequestMagic)
		if err != nil {
			return
		}
		request, err := whale.ParseRequest(header)
		if err != nil {
			return
		}
		var result whale.Result
		switch request.Operation {
		case whale.OpGetDiscover:
			result, err = service.Discover(context.Background(), clientID, request)
		case whale.OpGetQuote:
			result, err = service.Quote(context.Background(), clientID, request)
		case whale.OpGetWindow:
			mu.Lock()
			windows++
			mu.Unlock()
			_, err = service.ServeWindow(context.Background(), clientID, request, func(result whale.Result, payload io.Reader) error {
				writeWhaleResponse(channel, whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "ok", Result: &result})
				_, err := io.Copy(channel, payload)
				return err
			})
			if err != nil {
				t.Errorf("serve window: %v", err)
			}
			return
		case whale.OpGetRelease:
			mu.Lock()
			releases = append(releases, request)
			first := len(releases) == 1
			mu.Unlock()
			if first && mode != "lost" {
				close(started)
				<-unblock
			}
			result, err = service.Release(context.Background(), clientID, request)
			if first && mode == "lost" {
				if err != nil {
					t.Errorf("release: %v", err)
				}
				return
			}
		default:
			t.Errorf("unexpected operation %s", request.Operation)
			return
		}
		if err != nil {
			t.Errorf("worker: %v", err)
			return
		}
		writeWhaleResponse(channel, whale.Response{Schema: whale.Schema, RequestID: request.RequestID, Operation: request.Operation, Status: "ok", Result: &result})
	})
	block, err := ssh.MarshalPrivateKey(clientPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	key, known := filepath.Join(root, "identity"), filepath.Join(root, "known_hosts")
	if err := os.WriteFile(key, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{address}, hostSigner.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	profile := clientprofile.Profile{Schema: clientprofile.Schema, ServerID: "fixture", Address: host, ClientID: clientID, IdentityFile: key, KnownHosts: known, SSHPort: port, ServiceURL: "svn+ssh://_filees-client@127.0.0.1/filees-service", ServiceWC: filepath.Join(root, "service"), CachePath: filepath.Join(root, "view.json"), RelativeViewPath: "clients/" + clientID + "/view.json", PollInterval: time.Minute}
	profilePath := filepath.Join(root, "profile.json")
	if err := clientprofile.Store(profilePath, profile); err != nil {
		t.Fatal(err)
	}
	state, dest := filepath.Join(root, "state"), filepath.Join(root, "received.bin")
	command := func(action string, args ...string) *exec.Cmd {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		t.Cleanup(cancel)
		base := []string{action, "--profile", profilePath, "--state", state, "--timeout", "15s"}
		return exec.CommandContext(ctx, binary, append(base, args...)...)
	}
	output, err := command("whale-get-quote", "--repo", identity.LogicalRepoID, "--path", identity.LogicalPath, "--revision", "7", "--dest", dest).CombinedOutput()
	if err != nil {
		t.Fatalf("quote: %v %s", err, output)
	}
	id := strings.TrimPrefix(strings.Fields(string(output))[0], "operation_id=")
	if _, err := uuid.Parse(id); err != nil {
		t.Fatal("invalid operation ID")
	}
	manager, err := NewManager(state, []clientprofile.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	confirm := command("whale-get-confirm", "--id", id)
	var confirmOutput bytes.Buffer
	confirm.Stdout = &confirmOutput
	confirm.Stderr = &confirmOutput
	if err := confirm.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- confirm.Wait() }()
	if mode == "lost" {
		if err := <-done; err == nil {
			t.Fatal("lost receipt incorrectly returned success")
		}
		if op, err := manager.Get(id); err != nil || op.State != StateFailed {
			t.Fatalf("failure not persisted: %+v %v", op, err)
		}
	} else {
		select {
		case <-started:
		case err := <-done:
			t.Fatalf("CLI exited before release: %v %s", err, confirmOutput.String())
		case <-time.After(10 * time.Second):
			t.Fatal("release not reached")
		}
		if op, err := manager.Get(id); err != nil || op.State != StateVerifying {
			t.Fatalf("premature success: %+v %v", op, err)
		}
		for _, path := range []string{filepath.Join(cache, id, "payload.ready"), filepath.Join(ledger, id+".json")} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("pending resources absent: %s: %v", path, err)
			}
		}
		select {
		case err := <-done:
			t.Fatalf("CLI left before receipt: %v", err)
		case <-time.After(400 * time.Millisecond):
		}
		if mode == "killed" {
			if err := confirm.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			<-done
		}
		once.Do(func() { close(unblock) })
		if mode == "delayed" {
			if err := <-done; err != nil {
				t.Fatalf("confirm: %v %s", err, confirmOutput.String())
			}
		}
	}
	if mode != "delayed" {
		action := "whale-retry"
		if mode == "killed" {
			action = "whale-resume"
		}
		output, err := command(action, "--id", id).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", action, err, output)
		}
	}
	if op, err := manager.Get(id); err != nil || op.State != StateLocal {
		t.Fatalf("not complete: %+v %v", op, err)
	}
	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, content) {
		t.Fatal("destination differs")
	}
	for _, path := range []string{filepath.Join(cache, id), filepath.Join(ledger, id+".json")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful CLI retained server resource %s: %v", path, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	wantReleases := 2
	if mode == "delayed" {
		wantReleases = 1
	}
	if windows != 1 || len(releases) != wantReleases {
		t.Fatalf("windows=%d releases=%d", windows, len(releases))
	}
	for _, request := range releases {
		if request.TransferID != id || request.ConfirmationToken != releases[0].ConfirmationToken {
			t.Fatal("release tuple changed")
		}
	}
}

func releaseCLIEndpoint(t *testing.T, signer ssh.Signer, clientKey ssh.PublicKey, handle func(ssh.Channel)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !publicKeysEqual(key, clientKey) {
			return nil, errors.New("wrong fixture key")
		}
		return &ssh.Permissions{}, nil
	}}
	cfg.AddHostKey(signer)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
				_, channels, requests, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for next := range channels {
					if next.ChannelType() != "session" {
						_ = next.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, requests, err := next.Accept()
					if err != nil {
						return
					}
					defer channel.Close()
					for request := range requests {
						var payload struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil || payload.Command != "filees whale-v1" {
							_ = request.Reply(false, nil)
							continue
						}
						_ = request.Reply(true, nil)
						handle(channel)
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().String()
}
