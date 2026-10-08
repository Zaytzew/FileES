package whaleclient

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	whale "filees/pkg/whale/v1"
)

// A controllable receipt models delay, transport failure and loss of the
// response after the server has applied an idempotent release.
type releaseExchange struct {
	base       actorExchange
	started    chan struct{}
	unblock    chan struct{}
	mu         sync.Mutex
	requests   []whale.Request
	firstError error
}

func (e *releaseExchange) Do(ctx context.Context, request whale.Request, payload io.Reader, receive ...io.Writer) (whale.Response, error) {
	if request.Operation != whale.OpGetRelease {
		return e.base.Do(ctx, request, payload, receive...)
	}
	e.mu.Lock()
	e.requests = append(e.requests, request)
	first := len(e.requests) == 1
	e.mu.Unlock()
	if first && e.started != nil {
		close(e.started)
		select {
		case <-e.unblock:
		case <-ctx.Done():
			return whale.Response{}, ctx.Err()
		}
	}
	response, err := e.base.Do(ctx, request, payload, receive...)
	if first && e.firstError != nil {
		return whale.Response{}, e.firstError
	}
	return response, err
}
func confirmedReleaseFixture(t *testing.T, e *releaseExchange) (*Manager, Operation, string) {
	t.Helper()
	m := actorManager(t, filepath.Join(t.TempDir(), "state"), e)
	dest := filepath.Join(t.TempDir(), "file.bin")
	op, err := m.BeginGetTarget(context.Background(), "office", "ddef25b5-fe28-5998-8abe-deca68dd8917", "media/file.bin", 7, dest)
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, m, op.OperationID, StateAwaitingConfirmation)
	if _, err = m.ConfirmGet(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	return m, op, dest
}
func TestGetWaitsForReleaseReceiptBeforeLocal(t *testing.T) {
	e := &releaseExchange{base: actorExchange{content: []byte("received bytes")}, started: make(chan struct{}), unblock: make(chan struct{})}
	var unblock sync.Once
	defer unblock.Do(func() { close(e.unblock) })
	m, op, dest := confirmedReleaseFixture(t, e)
	select {
	case <-e.started:
	case <-time.After(5 * time.Second):
		t.Fatal("release did not start")
	}
	current, err := m.Get(op.OperationID)
	if err != nil || current.State != StateVerifying {
		t.Fatalf("premature success: %+v %v", current, err)
	}
	if b, err := os.ReadFile(dest); err != nil || string(b) != "received bytes" {
		t.Fatalf("bytes=%q err=%v", b, err)
	}
	// This state survives a one-shot process exit; it is not terminal.
	restarted := actorManager(t, m.Root, e)
	persisted, err := restarted.Get(op.OperationID)
	if err != nil || persisted.State != StateVerifying {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	unblock.Do(func() { close(e.unblock) })
	waitOperation(t, m, op.OperationID, StateLocal)
}
func TestGetReleaseFailureRetriesWithoutRedownload(t *testing.T) {
	e := &releaseExchange{base: actorExchange{content: []byte("received bytes")}, firstError: errors.New("lost release receipt")}
	m, op, dest := confirmedReleaseFixture(t, e)
	failed := waitOperation(t, m, op.OperationID, StateFailed)
	if failed.BytesHave != int64(len(e.base.content)) {
		t.Fatalf("failed=%+v", failed)
	}
	if _, err := m.Retry(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	waitOperation(t, m, op.OperationID, StateLocal)
	if b, err := os.ReadFile(dest); err != nil || string(b) != "received bytes" {
		t.Fatalf("bytes=%q err=%v", b, err)
	}
	e.base.mu.Lock()
	windows := e.base.getWindows
	e.base.mu.Unlock()
	e.mu.Lock()
	requests := append([]whale.Request(nil), e.requests...)
	e.mu.Unlock()
	if windows != 1 || len(requests) != 2 {
		t.Fatalf("windows=%d releases=%d", windows, len(requests))
	}
	if requests[0].TransferID != requests[1].TransferID || requests[0].ConfirmationToken != requests[1].ConfirmationToken {
		t.Fatal("release identity changed on retry")
	}
}
func TestGetResumeReplaysReleaseAfterProcessCancellation(t *testing.T) {
	e := &releaseExchange{base: actorExchange{content: []byte("received bytes")}, firstError: context.Canceled}
	m, op, dest := confirmedReleaseFixture(t, e)
	if err := m.waitIdle(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	persisted, err := m.Get(op.OperationID)
	if err != nil || persisted.State != StateVerifying {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	restarted := actorManager(t, m.Root, e)
	if err := restarted.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitOperation(t, restarted, op.OperationID, StateLocal)
	if b, err := os.ReadFile(dest); err != nil || string(b) != "received bytes" {
		t.Fatalf("bytes=%q err=%v", b, err)
	}
	e.base.mu.Lock()
	windows := e.base.getWindows
	e.base.mu.Unlock()
	e.mu.Lock()
	releases := len(e.requests)
	e.mu.Unlock()
	if windows != 1 || releases != 2 {
		t.Fatalf("windows=%d releases=%d", windows, releases)
	}
}
