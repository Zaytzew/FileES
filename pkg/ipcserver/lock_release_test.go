package ipcserver

import (
	"context"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
)

type lockReleaseServiceStub struct {
	requested contract.LockReleaseRequestPayload
	decided   contract.LockReleaseDecisionPayload
	action    string
	onAction  func()
}

func TestLockReleaseCapabilitiesAreAdvertisedOnlyWhenWired(t *testing.T) {
	server := New("unused")
	capabilities := []string{contract.CapLockReleaseRequest, contract.CapLockReleaseDismiss, contract.CapLockReleaseAccept}
	for _, capability := range capabilities {
		if containsCapability(server.capabilities(), capability) {
			t.Fatalf("unwired capability advertised: %s", capability)
		}
	}
	server.SetLockReleaseService(&lockReleaseServiceStub{})
	for _, capability := range capabilities {
		if !containsCapability(server.capabilities(), capability) {
			t.Fatalf("wired capability missing: %s", capability)
		}
	}
}

func (stub *lockReleaseServiceStub) Request(_ context.Context, payload contract.LockReleaseRequestPayload) (contract.LockReleaseRequest, error) {
	stub.requested = payload
	return contract.LockReleaseRequest{RequestID: "request-1", ServerID: payload.ServerID, RepoID: payload.RepoID, Path: payload.Path, ObservedLockID: payload.ObservedLockID, Role: "requester", State: "pending"}, nil
}

func (stub *lockReleaseServiceStub) Dismiss(_ context.Context, payload contract.LockReleaseDecisionPayload) (contract.LockReleaseRequest, error) {
	stub.decided, stub.action = payload, "dismiss"
	if stub.onAction != nil {
		stub.onAction()
	}
	return contract.LockReleaseRequest{RequestID: payload.RequestID, ServerID: payload.ServerID, State: "dismissed"}, nil
}

func (stub *lockReleaseServiceStub) Accept(_ context.Context, payload contract.LockReleaseDecisionPayload) (contract.LockReleaseRequest, error) {
	stub.decided, stub.action = payload, "accept"
	if stub.onAction != nil {
		stub.onAction()
	}
	return contract.LockReleaseRequest{RequestID: payload.RequestID, ServerID: payload.ServerID, State: "accepted"}, nil
}

func TestLockReleaseRequestRequiresCurrentlyObservedForeignToken(t *testing.T) {
	server := New("unused")
	service := &lockReleaseServiceStub{}
	server.SetLockReleaseService(service)
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{Reservations: []contract.Reservation{{RepoID: "docs", Path: "plans/a.dwg", Token: "opaque-token", CanRelease: false}}}, nil
	}, nil)
	payload := contract.LockReleaseRequestPayload{ServerID: "office", RepoID: "docs", Path: "plans/a.dwg", ObservedLockID: "opaque-token"}
	response := server.dispatch(lifecycleRequest(contract.CmdLockReleaseRequest, payload))
	if response.Status != contract.StatusOK || service.requested != payload {
		t.Fatalf("response=%+v forwarded=%+v", response.Error, service.requested)
	}

	payload.ObservedLockID = "later-token"
	response = server.dispatch(lifecycleRequest(contract.CmdLockReleaseRequest, payload))
	if response.Status == contract.StatusOK || service.requested.ObservedLockID == "later-token" {
		t.Fatalf("stale token reached control plane: response=%+v forwarded=%+v", response, service.requested)
	}
}

func TestReservationPathIsCanonicalPOSIXOnEveryHost(t *testing.T) {
	valid := []string{"a.txt", "plans/a.dwg", "deep/project/model/file.dwg"}
	for _, value := range valid {
		if !validReservationPath(value) {
			t.Errorf("canonical POSIX path rejected: %q", value)
		}
	}
	invalid := []string{"", ".", "..", "../a.txt", "plans/../a.dwg", "/plans/a.dwg", "plans\\a.dwg", "plans//a.dwg", " plans/a.dwg", "plans/a.dwg "}
	for _, value := range invalid {
		if validReservationPath(value) {
			t.Errorf("non-canonical protocol path accepted: %q", value)
		}
	}
}

func TestLockReleaseRequestDoesNotAskCurrentHolder(t *testing.T) {
	server := New("unused")
	service := &lockReleaseServiceStub{}
	server.SetLockReleaseService(service)
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{Reservations: []contract.Reservation{{RepoID: "docs", Path: "a.txt", Token: "mine", CanRelease: true}}}, nil
	}, nil)
	response := server.dispatch(lifecycleRequest(contract.CmdLockReleaseRequest, contract.LockReleaseRequestPayload{ServerID: "office", RepoID: "docs", Path: "a.txt", ObservedLockID: "mine"}))
	if response.Status == contract.StatusOK || service.requested.ServerID != "" {
		t.Fatalf("own lock request response=%+v forwarded=%+v", response, service.requested)
	}
}

func TestLockReleaseAcceptChecksProjectionAndReleasesExactTokenAfterConsent(t *testing.T) {
	server := New("unused")
	service := &lockReleaseServiceStub{}
	server.SetLockReleaseService(service)
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{Reservations: []contract.Reservation{{RepoID: "docs", Path: "plans/a.dwg", Token: "opaque-token", CanRelease: true}}}, nil
	}, func(_ context.Context, path, token string, confirmRisk bool) error {
		if service.action != "accept" {
			return errors.New("unlock ran before remote consent")
		}
		if path != "plans/a.dwg" || token != "opaque-token" || !confirmRisk {
			return errors.New("unlock lost its fencing data")
		}
		return nil
	})
	server.SetLockReleaseProjection("office", []contract.LockReleaseRequest{{
		RequestID: "request-1", ServerID: "office", RepoID: "docs", Path: "plans/a.dwg",
		ObservedLockID: "opaque-token", Role: "holder", State: "pending",
	}})
	response := server.dispatch(lifecycleRequest(contract.CmdLockReleaseAccept, contract.LockReleaseDecisionPayload{ServerID: "office", RequestID: "request-1"}))
	if response.Status != contract.StatusOK || service.action != "accept" || service.decided.RequestID != "request-1" {
		t.Fatalf("accept response=%+v service=%+v", response.Error, service)
	}
}

func TestLockReleaseDismissDoesNotUnlock(t *testing.T) {
	server := New("unused")
	service := &lockReleaseServiceStub{}
	server.SetLockReleaseService(service)
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	released := false
	repo.SetReservationFuncs(nil, func(context.Context, string, string, bool) error { released = true; return nil })
	server.SetLockReleaseProjection("office", []contract.LockReleaseRequest{{RequestID: "request-1", ServerID: "office", RepoID: "docs", Path: "a.txt", ObservedLockID: "token", Role: "holder", State: "pending"}})
	response := server.dispatch(lifecycleRequest(contract.CmdLockReleaseDismiss, contract.LockReleaseDecisionPayload{ServerID: "office", RequestID: "request-1"}))
	if response.Status != contract.StatusOK || service.action != "dismiss" || released {
		t.Fatalf("dismiss response=%+v action=%q released=%v", response.Error, service.action, released)
	}
}

func TestSystemStatusIncludesPrivateLockReleaseProjection(t *testing.T) {
	server := New("unused")
	server.SetLockReleaseProjection("office", []contract.LockReleaseRequest{{RequestID: "older", ServerID: "office", UpdatedAt: "2026-08-31T10:00:00Z"}})
	server.SetLockReleaseProjection("home", []contract.LockReleaseRequest{{RequestID: "newer", ServerID: "home", UpdatedAt: "2026-08-31T11:00:00Z"}})
	response := server.dispatch(lifecycleRequest(contract.CmdSystemStatus, nil))
	var result contract.SystemStatusResult
	decodeIPCResult(t, response, &result)
	if len(result.LockReleaseRequests) != 2 || result.LockReleaseRequests[0].RequestID != "newer" {
		t.Fatalf("lock release projection=%+v", result.LockReleaseRequests)
	}
}
