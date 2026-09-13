package contracttests

import (
	"context"
	"encoding/json"
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
