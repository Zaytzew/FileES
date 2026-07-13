package contracttests

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

type fakeDaemon func(contract.Request) contract.Response

func startFakeDaemon(t *testing.T, handle fakeDaemon) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req contract.Request
				if json.NewDecoder(c).Decode(&req) != nil {
					return
				}
				_ = json.NewEncoder(c).Encode(handle(req))
			}(conn)
		}
	}()
	return sock
}

func TestIPCClientHelloEnvelopeAndResult(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		if req.Protocol != contract.Protocol {
			t.Errorf("protocol = %q", req.Protocol)
		}
		if req.RequestID == "" {
			t.Error("missing request ID")
		}
		if req.ClientID != "test-client" {
			t.Errorf("client ID = %q", req.ClientID)
		}
		if req.Command != contract.CmdSystemHello {
			t.Errorf("command = %q", req.Command)
		}
		return contract.OKResponse(req.RequestID, contract.HelloResult{
			DaemonVersion:    "test",
			ProtocolVersions: []string{contract.Protocol},
			Capabilities:     []string{contract.CmdRepoStatus},
		})
	})

	result, err := ipcclient.New(sock, "test-client").Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.DaemonVersion != "test" || len(result.Capabilities) != 1 || result.Capabilities[0] != contract.CmdRepoStatus {
		t.Fatalf("Hello() = %#v", result)
	}
}

func TestIPCClientRepoStatusUsesRepoID(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		if req.Command != contract.CmdRepoStatus || req.RepoID != "projectA" {
			t.Errorf("request = %#v", req)
		}
		return contract.OKResponse(req.RequestID, contract.RepoStatus{
			RepoID:       req.RepoID,
			State:        contract.StateActive,
			Connectivity: contract.ConnOnline,
		})
	})

	result, err := ipcclient.New(sock, "test-client").RepoStatus(context.Background(), "projectA")
	if err != nil {
		t.Fatal(err)
	}
	if result.RepoID != "projectA" || result.State != contract.StateActive {
		t.Fatalf("RepoStatus() = %#v", result)
	}
}

func TestIPCClientReturnsContractError(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		return contract.ErrResponse(req.RequestID, "PROTO-0005", "ERROR", "NONE", "proto.repo_not_found", nil)
	})

	_, err := ipcclient.New(sock, "test-client").RepoStatus(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "PROTO-0005") || !strings.Contains(err.Error(), "proto.repo_not_found") {
		t.Fatalf("RepoStatus() error = %v", err)
	}
}

func TestIPCClientRejectsMismatchedProtocol(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		resp := contract.OKResponse(req.RequestID, contract.RepoListResult{})
		resp.Protocol = "filees.contract/v2"
		return resp
	})

	_, err := ipcclient.New(sock, "test-client").RepoList(context.Background())
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("RepoList() error = %v", err)
	}
}

func TestIPCClientRejectsMismatchedRequestID(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		return contract.OKResponse(req.RequestID+"-other", contract.RepoListResult{})
	})

	_, err := ipcclient.New(sock, "test-client").RepoList(context.Background())
	if err == nil || !strings.Contains(err.Error(), "request_id mismatch") {
		t.Fatalf("RepoList() error = %v", err)
	}
}

func TestIPCClientRejectsUnknownResponseStatus(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		resp := contract.OKResponse(req.RequestID, contract.RepoListResult{})
		resp.Status = "future-status"
		return resp
	})

	_, err := ipcclient.New(sock, "test-client").RepoList(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("RepoList() error = %v", err)
	}
}

func TestIPCClientDaemonUnavailable(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "missing.sock")
	_, err := ipcclient.New(sock, "test-client").RepoList(context.Background())
	if err == nil || !strings.Contains(err.Error(), "daemon unreachable") {
		t.Fatalf("RepoList() error = %v", err)
	}
}

func TestIPCClientLockPayload(t *testing.T) {
	wantPaths := []string{"/workspace/projectA/model.dwg"}
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		if req.Command != contract.CmdRepoLock || req.RepoID != "projectA" {
			t.Errorf("request = %#v", req)
		}
		var payload contract.RepoLockPayload
		if err := contract.DecodePayload(req.Payload, &payload); err != nil {
			t.Error(err)
		}
		if len(payload.Paths) != 1 || payload.Paths[0] != wantPaths[0] {
			t.Errorf("payload = %#v", payload)
		}
		return contract.OKResponse(req.RequestID, contract.LockResult{Output: "locked"})
	})

	output, err := ipcclient.New(sock, "test-client").Lock(context.Background(), "projectA", wantPaths)
	if err != nil {
		t.Fatal(err)
	}
	if output != "locked" {
		t.Fatalf("Lock() output = %q", output)
	}
}

func TestIPCServerClientRoundTrip(t *testing.T) {
	wc := t.TempDir()
	stateDir := filepath.Join(wc, ".filees", "state")
	cacheDir := filepath.Join(wc, ".filees", "commit_cache")
	logDir := filepath.Join(wc, ".filees", "logs")
	for _, dir := range []string{stateDir, cacheDir, logDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stateDir, "head.rev"), []byte("17\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := `[{"op":"added"},{"op":"modified"},{"op":"deleted"}]`
	if err := os.WriteFile(filepath.Join(cacheDir, "cache.json"), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	logLine := `{"ts":"2026-07-13T10:00:00Z","scope":"commit:projectA","code":"NET-4007","severity":"WARN","hint":"RETRY_BACKOFF","msg":"offline","details":"network"}`
	if err := os.WriteFile(filepath.Join(logDir, "errors.jsonl"), []byte(logLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(sock)
	repo := server.RegisterRepo("projectA", "svn://example/projectA", wc)
	repo.SetState(contract.StateActive)
	repo.SetLockFuncs(
		func(_ context.Context, paths []string) (string, error) {
			if len(paths) != 1 || paths[0] != filepath.Join(wc, "model.dwg") {
				t.Errorf("lock paths = %#v", paths)
			}
			return "locked", nil
		},
		func(_ context.Context, paths []string) (string, error) {
			if len(paths) != 1 || paths[0] != filepath.Join(wc, "model.dwg") {
				t.Errorf("unlock paths = %#v", paths)
			}
			return "unlocked", nil
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(cancel)

	cli := ipcclient.New(sock, "integration-test")
	hello, err := cli.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hello.ProtocolVersions) != 1 || hello.ProtocolVersions[0] != contract.Protocol {
		t.Fatalf("hello = %#v", hello)
	}

	list, err := cli.RepoList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Repos) != 1 || list.Repos[0].ID != "projectA" {
		t.Fatalf("repo list = %#v", list)
	}

	status, err := cli.RepoStatus(context.Background(), "projectA")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != contract.StateActive || status.LocalRevision != 17 || status.HeadRevision != 17 {
		t.Fatalf("repo status = %#v", status)
	}
	if status.Pending.Added != 1 || status.Pending.Modified != 1 || status.Pending.Deleted != 1 {
		t.Fatalf("pending stats = %#v", status.Pending)
	}

	path := filepath.Join(wc, "model.dwg")
	if output, err := cli.Lock(context.Background(), "projectA", []string{path}); err != nil || output != "locked" {
		t.Fatalf("Lock() output = %q, error = %v", output, err)
	}
	if output, err := cli.Unlock(context.Background(), "projectA", []string{path}); err != nil || output != "unlocked" {
		t.Fatalf("Unlock() output = %q, error = %v", output, err)
	}

	errorsResult, err := cli.ErrorList(context.Background(), contract.ErrorListPayload{RepoID: "projectA", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(errorsResult.Errors) != 1 || errorsResult.Errors[0].Code != "NET-4007" {
		t.Fatalf("errors = %#v", errorsResult.Errors)
	}
}

func TestIPCServerRejectsUnknownCommand(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	cli := ipcclient.New(sock, "test-client")
	resp, err := cli.Do(context.Background(), contract.Request{
		Protocol:  contract.Protocol,
		RequestID: "request-unknown",
		ClientID:  "test-client",
		Command:   "future.command",
		Payload:   json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusError || resp.Error == nil || resp.Error.Code != "PROTO-0003" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestIPCServerRejectsInvalidLockPayload(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(sock)
	server.RegisterRepo("projectA", "svn://example/projectA", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	cli := ipcclient.New(sock, "test-client")
	payload, _ := json.Marshal(contract.RepoLockPayload{})
	resp, err := cli.Do(context.Background(), contract.Request{
		Protocol:  contract.Protocol,
		RequestID: "request-invalid-lock",
		ClientID:  "test-client",
		Command:   contract.CmdRepoLock,
		RepoID:    "projectA",
		Payload:   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusError || resp.Error == nil || resp.Error.MessageKey != "proto.invalid_payload" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestIPCServerRejectsLockPathOutsideWorkingCopy(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(sock)
	wc := t.TempDir()
	server.RegisterRepo("projectA", "svn://example/projectA", wc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	cli := ipcclient.New(sock, "test-client")
	_, err := cli.Lock(context.Background(), "projectA", []string{filepath.Join(filepath.Dir(wc), "outside.dwg")})
	if err == nil || !strings.Contains(err.Error(), "LOCK-2002") || !strings.Contains(err.Error(), "lock.invalid_path") {
		t.Fatalf("Lock() error = %v", err)
	}
}

func TestIPCServerRejectsRelativeLockPath(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(sock)
	server.RegisterRepo("projectA", "svn://example/projectA", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	cli := ipcclient.New(sock, "test-client")
	_, err := cli.Lock(context.Background(), "projectA", []string{"relative/model.dwg"})
	if err == nil || !strings.Contains(err.Error(), "LOCK-2002") || !strings.Contains(err.Error(), "lock.invalid_path") {
		t.Fatalf("Lock() error = %v", err)
	}
}

// --- helpers for event subscription tests ---

func startEventServer(t *testing.T) (*ipcserver.Server, *ipcserver.RepoState, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "events.sock")
	srv := ipcserver.New(sock)
	rs := srv.RegisterRepo("testRepo", "svn://x/y", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	return srv, rs, sock
}

// TestEventsSubscribeStreamingResponse verifies the subscribe ACK carries streaming:true.
func TestEventsSubscribeStreamingResponse(t *testing.T) {
	_, _, sock := startEventServer(t)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := contract.Request{
		Protocol:  contract.Protocol,
		RequestID: "sub-ack-1",
		ClientID:  "test",
		Command:   contract.CmdEventsSubscribe,
		Payload:   json.RawMessage("{}"),
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp contract.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusOK {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
	if resp.RequestID != req.RequestID {
		t.Fatalf("request_id mismatch")
	}
	var result struct {
		Streaming bool `json:"streaming"`
	}
	if err := contract.DecodeResult(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Streaming {
		t.Fatal("streaming not true in subscribe ACK")
	}
}

// TestEventsSubscribeDeliversStateChange verifies that SetState emits a correctly-shaped event.
func TestEventsSubscribeDeliversStateChange(t *testing.T) {
	_, rs, sock := startEventServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evCh, err := ipcclient.New(sock, "test-events").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(20 * time.Millisecond) // let subscription register server-side

	rs.SetState(contract.StateActive) // initial state is Initializing → triggers event

	select {
	case ev := <-evCh:
		if ev.Type != contract.EvRepoStateChanged {
			t.Fatalf("type = %q, want %q", ev.Type, contract.EvRepoStateChanged)
		}
		if ev.RepoID != "testRepo" {
			t.Fatalf("repo_id = %q", ev.RepoID)
		}
		if ev.Sequence <= 0 {
			t.Fatalf("sequence = %d, want > 0", ev.Sequence)
		}
		if ev.EventID == "" {
			t.Fatal("event_id empty")
		}
		var pl contract.RepoStateChangedPayload
		if err := contract.DecodePayload(ev.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		if pl.NewState != contract.StateActive {
			t.Fatalf("new_state = %q, want %q", pl.NewState, contract.StateActive)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for state-change event")
	}
}

// TestEventsSubscribeMonotoneSequence emits events from two goroutines and checks
// that the single subscriber always sees strictly increasing sequence numbers.
func TestEventsSubscribeMonotoneSequence(t *testing.T) {
	_, rs, sock := startEventServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	evCh, err := ipcclient.New(sock, "test-seq").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(20 * time.Millisecond)

	states := []string{contract.StateActive, contract.StateOffline}
	const N = 30
	done := make(chan struct{})
	go func() {
		for i := 0; i < N; i++ {
			rs.SetState(states[i%2])
		}
		close(done)
	}()
	for i := 0; i < N; i++ {
		rs.SetState(states[i%2])
	}
	<-done

	cancel() // closes the subscriber's connection
	var seqs []int64
	for ev := range evCh {
		seqs = append(seqs, ev.Sequence)
	}

	if len(seqs) < 2 {
		t.Skipf("too few events received (%d)", len(seqs))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("sequence not monotone at index %d: got %d after %d", i, seqs[i], seqs[i-1])
		}
	}
}

// TestEventsSubscribeSlowClientDropsEvents verifies that a non-reading subscriber
// does not block the daemon's Emit path.
func TestEventsSubscribeSlowClientDropsEvents(t *testing.T) {
	_, rs, sock := startEventServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evCh, err := ipcclient.New(sock, "slow-client").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = evCh // intentionally not reading

	time.Sleep(20 * time.Millisecond)

	states := []string{contract.StateActive, contract.StateOffline}
	emitDone := make(chan struct{})
	go func() {
		defer close(emitDone)
		for i := 0; i < 200; i++ {
			rs.SetState(states[i%2])
		}
	}()
	select {
	case <-emitDone:
		// Emit did not block — pass
	case <-time.After(5 * time.Second):
		t.Fatal("Emit blocked on slow subscriber")
	}
}

// TestEventsSubscribeChannelClosesOnCancel verifies the event channel is closed
// promptly when the subscriber context is cancelled.
func TestEventsSubscribeChannelClosesOnCancel(t *testing.T) {
	_, _, sock := startEventServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	evCh, err := ipcclient.New(sock, "disconnect-test").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case _, open := <-evCh:
		if open {
			t.Fatal("expected closed channel, got event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed after context cancellation")
	}
}

func TestIPCServerSocketPermissionsAndCleanup(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	server := ipcserver.New(sock)
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	info, err := os.Stat(sock)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		cancel()
		t.Fatalf("socket permissions = %o, want 600", perm)
	}

	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		_, err = os.Stat(sock)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket still exists after cancellation: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
