package contracttests

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
)

// Growing the transport buffer lazily must not lower the accepted frame size
// or remove support for consecutive requests on an active connection.
func TestIPCTransportLargeFrameAndFollowingRequest(t *testing.T) {
	sock := testSocketPath(t)
	s := ipcserver.New(sock)
	ctx, cancel := context.WithCancel(context.Background())
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
	enc, dec := json.NewEncoder(peer), json.NewDecoder(peer)
	for _, size := range []int{64 * 1024, 0} {
		req := contract.Request{Protocol: contract.Protocol, RequestID: "frame-test", ClientID: "contract-test", Command: contract.CmdSystemHello,
			Payload: json.RawMessage(`{"padding":"` + strings.Repeat("x", size) + `"}`)}
		if err := enc.Encode(req); err != nil {
			t.Fatal(err)
		}
		var resp contract.Response
		if err := dec.Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if resp.Protocol != contract.Protocol || resp.RequestID != req.RequestID || resp.Status != contract.StatusOK {
			t.Fatalf("response: %+v", resp)
		}
	}
}
