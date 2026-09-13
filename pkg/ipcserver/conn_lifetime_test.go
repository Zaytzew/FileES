package ipcserver

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

type slowAliasClaim struct{}

func (slowAliasClaim) Claim(context.Context, string, string) (string, error) {
	time.Sleep(250 * time.Millisecond)
	return "test", nil
}

func TestTransportDeadlineDoesNotLimitCommandExecution(t *testing.T) {
	s, peer, done := lifetimePair(t)
	s.SetRealmAliasService(slowAliasClaim{})
	s.RegisterActivation(contract.ActivationStatus{ServerID: "test"})
	req := lifetimeRequest(contract.CmdRealmAliasClaim)
	req.Payload = json.RawMessage(`{"server_id":"test","alias":"test"}`)
	if err := json.NewEncoder(peer).Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp contract.Response
	if err := json.NewDecoder(peer).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusOK {
		t.Fatalf("response: %+v", resp)
	}
	_ = peer.Close()
	awaitConnectionEnd(t, done)
}

func TestEventStreamBlockedWriteReleasesSubscriber(t *testing.T) {
	s, peer, done := lifetimePair(t)
	if err := json.NewEncoder(peer).Encode(lifetimeRequest(contract.CmdEventsSubscribe)); err != nil {
		t.Fatal(err)
	}
	var resp contract.Response
	if err := json.NewDecoder(peer).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	s.Emit(contract.Event{Type: "test.refresh"})
	awaitConnectionEnd(t, done)
	s.subsMu.Lock()
	count := len(s.subs)
	s.subsMu.Unlock()
	if count != 0 {
		t.Fatalf("retained subscribers: %d", count)
	}
}

// Exercise real blocked I/O with shortened transport deadlines; the peer stays
// open deliberately, reproducing the missing-EOF retention observed on Windows.
type shortDeadlineConn struct{ net.Conn }

func (c shortDeadlineConn) SetReadDeadline(d time.Time) error {
	if !d.IsZero() {
		d = time.Now().Add(100 * time.Millisecond)
	}
	return c.Conn.SetReadDeadline(d)
}
func (c shortDeadlineConn) SetWriteDeadline(d time.Time) error {
	if !d.IsZero() {
		d = time.Now().Add(100 * time.Millisecond)
	}
	return c.Conn.SetWriteDeadline(d)
}

func lifetimePair(t *testing.T) (*Server, net.Conn, <-chan struct{}) {
	t.Helper()
	s := New("unused")
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	done := make(chan struct{})
	go func() { defer close(done); s.handleConn(shortDeadlineConn{a}) }()
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	return s, b, done
}

func awaitConnectionEnd(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection retained after peer stopped making progress")
	}
}

func lifetimeRequest(command string) contract.Request {
	return contract.Request{Protocol: contract.Protocol, RequestID: "lifetime", ClientID: "test", Command: command}
}

func TestConnectionExpiresWithoutFirstFrame(t *testing.T) {
	_, _, done := lifetimePair(t)
	awaitConnectionEnd(t, done)
}

func TestConnectionExpiresAfterResponseWithoutPeerEOF(t *testing.T) {
	_, peer, done := lifetimePair(t)
	enc, dec := json.NewEncoder(peer), json.NewDecoder(peer)
	for i := 0; i < 2; i++ {
		if err := enc.Encode(lifetimeRequest(contract.CmdSystemHello)); err != nil {
			t.Fatal(err)
		}
		var resp contract.Response
		if err := dec.Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if resp.Status != contract.StatusOK {
			t.Fatalf("response: %+v", resp)
		}
	}
	awaitConnectionEnd(t, done)
}

func TestConnectionExpiresWhenPeerDoesNotReadResponse(t *testing.T) {
	_, peer, done := lifetimePair(t)
	if err := json.NewEncoder(peer).Encode(lifetimeRequest(contract.CmdSystemHello)); err != nil {
		t.Fatal(err)
	}
	awaitConnectionEnd(t, done)
}

func TestConnectionFrameGrowsPastInitialBuffer(t *testing.T) {
	_, peer, done := lifetimePair(t)
	req := lifetimeRequest(contract.CmdSystemHello)
	req.Payload = json.RawMessage(`{"padding":"` + strings.Repeat("x", 64*1024) + `"}`)
	if err := json.NewEncoder(peer).Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp contract.Response
	if err := json.NewDecoder(peer).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusOK {
		t.Fatalf("response: %+v", resp)
	}
	_ = peer.Close()
	awaitConnectionEnd(t, done)
}

func TestEventStreamSurvivesQuietReadInterval(t *testing.T) {
	s, peer, done := lifetimePair(t)
	if err := json.NewEncoder(peer).Encode(lifetimeRequest(contract.CmdEventsSubscribe)); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(peer)
	var resp contract.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("subscription closed")
	case <-time.After(250 * time.Millisecond):
	}
	s.Emit(contract.Event{Type: "test.refresh"})
	var event contract.Event
	if err := dec.Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "test.refresh" {
		t.Fatalf("event: %+v", event)
	}
	_ = peer.Close()
	awaitConnectionEnd(t, done)
	s.subsMu.Lock()
	count := len(s.subs)
	s.subsMu.Unlock()
	if count != 0 {
		t.Fatalf("retained subscribers: %d", count)
	}
}

func TestEventSubscribeFailedAcknowledgementReleasesSubscriber(t *testing.T) {
	s, peer, done := lifetimePair(t)
	if err := json.NewEncoder(peer).Encode(lifetimeRequest(contract.CmdEventsSubscribe)); err != nil {
		t.Fatal(err)
	}
	awaitConnectionEnd(t, done)
	s.subsMu.Lock()
	count := len(s.subs)
	s.subsMu.Unlock()
	if count != 0 {
		t.Fatalf("retained subscribers: %d", count)
	}
}
