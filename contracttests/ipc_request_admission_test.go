package contracttests

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
)

func TestIPCRequestAdmissionRefusalAndResume(t *testing.T) {
	sock := testSocketPath(t)
	s := ipcserver.New(sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	resume, err := s.QuiesceRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	peer, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	enc, dec := json.NewEncoder(peer), json.NewDecoder(peer)
	call := func(command string) contract.Response {
		t.Helper()
		req := contract.Request{Protocol: contract.Protocol, RequestID: command, ClientID: "contract-test", Command: command}
		if err := enc.Encode(req); err != nil {
			t.Fatal(err)
		}
		var resp contract.Response
		if err := dec.Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if resp.Protocol != contract.Protocol || resp.RequestID != req.RequestID {
			t.Fatalf("envelope: %+v", resp)
		}
		return resp
	}
	for _, command := range []string{contract.CmdRepoPublish, contract.CmdSystemRestart} {
		resp := call(command)
		if resp.Error == nil || resp.Error.Code != "SYSTEM-0002" || resp.Error.MessageKey != "system.quiescing" || resp.Error.Hint != "RETRY_BACKOFF" {
			t.Fatalf("refusal: %+v", resp)
		}
	}
	for _, command := range []string{contract.CmdSystemHello, contract.CmdSystemStatus} {
		if resp := call(command); resp.Status != contract.StatusOK {
			t.Fatalf("discovery: %+v", resp)
		}
	}
	resume()
	resp := call("future.mutation")
	if resp.Error == nil || resp.Error.MessageKey != "proto.unknown_command" {
		t.Fatalf("not resumed: %+v", resp)
	}
}

type heldLifecycle struct {
	entered chan struct{}
	finish  <-chan struct{}
}

func (l heldLifecycle) Restart()  { close(l.entered); <-l.finish }
func (l heldLifecycle) Shutdown() { l.Restart() }

func TestIPCDrainWaitsForAcknowledgedLifecycleCallback(t *testing.T) {
	sock := testSocketPath(t)
	s := ipcserver.New(sock)
	finish, entered := make(chan struct{}), make(chan struct{})
	defer close(finish)
	s.SetSystemLifecycleService(heldLifecycle{entered: entered, finish: finish})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	peer, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	req := contract.Request{Protocol: contract.Protocol, RequestID: "lifecycle", ClientID: "test", Command: contract.CmdSystemRestart}
	if err := json.NewEncoder(peer).Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp contract.Response
	if err := json.NewDecoder(peer).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusOK {
		t.Fatalf("ack: %+v", resp)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer drainCancel()
	resume, err := s.QuiesceRequests(drainCtx)
	if err == nil {
		resume()
		t.Fatal("drained while lifecycle callback still running")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
