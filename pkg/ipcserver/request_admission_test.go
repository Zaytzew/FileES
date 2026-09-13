package ipcserver

import (
	"context"
	"encoding/json"
	"errors"
	contract "filees/pkg/contract/v1"
	"sync"
	"testing"
	"time"
)

type admittedAlias struct {
	entered chan struct{}
	finish  <-chan struct{}
	server  *Server
}

type inspectingLifecycle struct{ check func() }

func (l inspectingLifecycle) Restart()  { l.check() }
func (l inspectingLifecycle) Shutdown() { l.check() }

func TestRequestAdmissionCoversResponseAndLifecycle(t *testing.T) {
	for _, writeFails := range []bool{false, true} {
		s := New("unused")
		called := false
		checkHeld := func() {
			t.Helper()
			if got := s.requestAdmission.Snapshot(); got.Active != 1 {
				t.Fatalf("lease not held: %+v", got)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
			defer cancel()
			if resume, err := s.QuiesceRequests(ctx); err == nil {
				resume()
				t.Fatal("drained before request completed")
			}
		}
		s.SetSystemLifecycleService(inspectingLifecycle{check: func() { called = true; checkHeld() }})
		writeErr := errors.New("peer stopped reading")
		err := s.executeRequest(contract.Request{RequestID: "restart", Command: contract.CmdSystemRestart}, func(value any) error {
			checkHeld()
			if resp := value.(contract.Response); resp.Status != contract.StatusOK {
				t.Fatalf("response: %+v", resp)
			}
			if writeFails {
				return writeErr
			}
			return nil
		})
		if writeFails && (!errors.Is(err, writeErr) || called) {
			t.Fatalf("failed response triggered lifecycle: err=%v called=%v", err, called)
		}
		if !writeFails && (err != nil || !called) {
			t.Fatalf("successful response: err=%v called=%v", err, called)
		}
		if got := s.requestAdmission.Snapshot(); got.Active != 0 {
			t.Fatalf("lease leaked: %+v", got)
		}
	}
}

func (a admittedAlias) Claim(ctx context.Context, _, alias string) (string, error) {
	close(a.entered)
	select {
	case <-a.finish:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	release, err := a.server.OperationAdmission().EnterContext(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return alias, nil
}

func TestRequestDrainWaitsForHandlerWithoutClosingWorkers(t *testing.T) {
	s := New("unused")
	s.RegisterActivation(contract.ActivationStatus{ServerID: "test"})
	entered, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	finishHandler := func() { finishOnce.Do(func() { close(finish) }) }
	defer finishHandler()
	s.SetRealmAliasService(admittedAlias{entered: entered, finish: finish, server: s})
	req := contract.Request{RequestID: "first", Command: contract.CmdRealmAliasClaim, Payload: json.RawMessage(`{"server_id":"test","alias":"test"}`)}
	response := make(chan contract.Response, 1)
	go func() { response <- s.dispatch(req) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		resume func()
		err    error
	}
	drained := make(chan result, 1)
	go func() { resume, err := s.QuiesceRequests(ctx); drained <- result{resume, err} }()
	for !s.requestAdmission.Snapshot().Closed {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	blocked := s.dispatch(req)
	if blocked.Error == nil || blocked.Error.MessageKey != "system.quiescing" {
		t.Fatalf("response: %+v", blocked)
	}
	select {
	case <-drained:
		t.Fatal("drained before handler result")
	default:
	}
	finishHandler()
	if got := <-response; got.Status != contract.StatusOK {
		t.Fatalf("accepted handler failed: %+v", got)
	}
	d := <-drained
	if d.err != nil {
		t.Fatal(d.err)
	}
	defer d.resume()
	if s.OperationAdmission().Snapshot().Closed {
		t.Fatal("ingress drain closed workers")
	}
}

func TestRequestDrainKeepsDiscoveryAndStatusAvailable(t *testing.T) {
	s := New("unused")
	resume, err := s.QuiesceRequests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	for _, command := range []string{contract.CmdSystemHello, contract.CmdSystemStatus, contract.CmdMessagesCatalog} {
		resp := s.dispatch(contract.Request{RequestID: "read", Command: command, Payload: json.RawMessage(`{"locale":"en"}`)})
		if resp.Status != contract.StatusOK {
			t.Fatalf("%s: %+v", command, resp)
		}
	}
	for _, command := range []string{contract.CmdRepoPublish, contract.CmdSystemRestart, "future.mutation"} {
		resp := s.dispatch(contract.Request{RequestID: "blocked", Command: command})
		if resp.Error == nil || resp.Error.Code != "SYSTEM-0002" || resp.Error.Hint != "RETRY_BACKOFF" {
			t.Fatalf("%s: %+v", command, resp)
		}
	}
	resume()
	resp := s.dispatch(contract.Request{RequestID: "resumed", Command: "future.mutation"})
	if resp.Error == nil || resp.Error.MessageKey != "proto.unknown_command" {
		t.Fatalf("not resumed: %+v", resp)
	}
}
