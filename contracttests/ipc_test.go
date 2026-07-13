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
