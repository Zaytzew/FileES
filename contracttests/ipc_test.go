package contracttests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

type fakeDaemon func(contract.Request) contract.Response

// testSocketPath stays under UNIX_PATH_MAX (108). t.TempDir() plus a long
// test name overflows that on Windows and bind() returns EINVAL.
func testSocketPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "feipc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	base := f.Name()
	f.Close()
	_ = os.Remove(base)
	sock := base + ".s"
	t.Cleanup(func() { _ = os.Remove(sock) })
	return sock
}

func startFakeDaemon(t *testing.T, handle fakeDaemon) string {
	t.Helper()
	sock := testSocketPath(t)
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

	sock := testSocketPath(t)
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
	sock := testSocketPath(t)
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
	sock := testSocketPath(t)
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
	sock := testSocketPath(t)
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
	var responseErr *ipcclient.ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("Lock() error type = %T, want *ipcclient.ResponseError", err)
	}
	if responseErr.Body.Code != "LOCK-2002" || responseErr.Body.Severity != "ERROR" || responseErr.Body.Hint != "REQUIRE_ACTION" {
		t.Fatalf("structured error = %#v", responseErr.Body)
	}
}

func TestIPCServerRejectsRelativeLockPath(t *testing.T) {
	sock := testSocketPath(t)
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
	sock := testSocketPath(t)
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

// TestEventsSubscribeMonotoneSequence requires every concurrently emitted event
// to arrive with a strictly increasing sequence number.
func TestEventsSubscribeMonotoneSequence(t *testing.T) {
	srv, _, sock := startEventServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evCh, err := ipcclient.New(sock, "test-seq").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	const total = 60
	var wg sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < total/2; i++ {
				srv.Emit(srv.NewRepoEvent("testRepo", contract.EvNoticeCreated,
					contract.NoticeCreatedPayload{NoticeID: fmt.Sprintf("%d-%d", worker, i)}))
			}
		}()
	}
	wg.Wait()

	seqs := make([]int64, 0, total)
	deadline := time.After(3 * time.Second)
	for len(seqs) < total {
		select {
		case ev, open := <-evCh:
			if !open {
				t.Fatalf("event stream closed after %d/%d events", len(seqs), total)
			}
			seqs = append(seqs, ev.Sequence)
		case <-deadline:
			t.Fatalf("received %d/%d events", len(seqs), total)
		}
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("sequence not monotone at index %d: got %d after %d", i, seqs[i], seqs[i-1])
		}
	}
}

// TestEventsSubscribeConcurrentStateTransitionsRemainCausal verifies that the
// payload chain follows the actual serialized RepoState mutations.
func TestEventsSubscribeConcurrentStateTransitionsRemainCausal(t *testing.T) {
	_, rs, sock := startEventServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evCh, err := ipcclient.New(sock, "test-state-order").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	states := []string{contract.StateActive, contract.StateOffline}
	var wg sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				rs.SetState(states[(i+offset)%len(states)])
			}
		}(worker)
	}
	wg.Wait()
	rs.SetState(contract.StateStopping)

	previous := contract.StateInitializing
	received := 0
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, open := <-evCh:
			if !open {
				t.Fatal("event stream closed before terminal state")
			}
			var payload contract.RepoStateChangedPayload
			if err := contract.DecodePayload(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.OldState != previous {
				t.Fatalf("non-causal transition at sequence %d: old_state=%q, previous new_state=%q",
					ev.Sequence, payload.OldState, previous)
			}
			previous = payload.NewState
			received++
			if payload.NewState == contract.StateStopping {
				if received < 3 {
					t.Fatalf("too few state transitions received: %d", received)
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for terminal state event")
		}
	}
}

func TestEventsSubscribeHandshakeHonorsContextCancellation(t *testing.T) {
	sock := testSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req contract.Request
		_ = json.NewDecoder(conn).Decode(&req)
		var oneByte [1]byte
		_, _ = conn.Read(oneByte[:]) // wait for client cancellation/close; never ACK
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = ipcclient.New(sock, "stalled-client").Subscribe(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Subscribe() error = %v, want context deadline exceeded", err)
	}
}

func TestEventsSubscribeRejectsInvalidStreamingACK(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		return contract.OKResponse(req.RequestID, map[string]bool{"streaming": false})
	})

	_, err := ipcclient.New(sock, "bad-ack-client").Subscribe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid streaming acknowledgement") {
		t.Fatalf("Subscribe() error = %v", err)
	}
}

func TestEventsSubscribeRejectsMalformedEventEnvelope(t *testing.T) {
	sock := testSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec := json.NewDecoder(conn)
		enc := json.NewEncoder(conn)
		var req contract.Request
		if dec.Decode(&req) != nil {
			return
		}
		_ = enc.Encode(contract.OKResponse(req.RequestID, map[string]bool{"streaming": true}))
		_ = enc.Encode(contract.Event{Protocol: "filees.contract/v2", EventID: "bad", Sequence: 1,
			Timestamp: time.Now().UTC().Format(time.RFC3339), Type: contract.EvOnlineRestored})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	evCh, err := ipcclient.New(sock, "malformed-client").Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ev, open := <-evCh; open {
		t.Fatalf("malformed event delivered to caller: %#v", ev)
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
	sock := testSocketPath(t)
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
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			cancel()
			t.Fatalf("socket permissions = %o, want 600", perm)
		}
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
