package repoworker

import (
	"context"
	"testing"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

type fakeLockReleaseAuthority struct {
	observation *LockReleaseObservation
	err         error
	repoID      string
	path        string
}

func (a *fakeLockReleaseAuthority) InspectLock(_ context.Context, repoID, path string) (*LockReleaseObservation, error) {
	a.repoID, a.path = repoID, path
	if a.observation == nil {
		return nil, a.err
	}
	copy := *a.observation
	return &copy, a.err
}

type lockReleaseWorkerFixture struct {
	worker              *Worker
	authority           *fakeLockReleaseAuthority
	requester, holder   Session
	repoID, path, token string
}

func newLockReleaseWorkerFixture(t *testing.T) lockReleaseWorkerFixture {
	t.Helper()
	repoID := uuid.NewString()
	repo := clientview.Repository{RepoID: repoID, Access: "rw", State: "active"}
	requester := Session{ClientID: uuid.NewString(), RealmID: uuid.NewString(), Repositories: []clientview.Repository{repo}}
	holder := Session{ClientID: uuid.NewString(), RealmID: uuid.NewString(), Repositories: []clientview.Repository{repo}}
	token := "opaquelocktoken:" + uuid.NewString()
	authority := &fakeLockReleaseAuthority{observation: &LockReleaseObservation{ObservedLockID: token, HolderClientID: holder.ClientID, HolderRealmID: holder.RealmID}}
	store := &FileLockReleaseStore{Root: t.TempDir(), TTL: 3 * time.Hour}
	return lockReleaseWorkerFixture{
		worker: &Worker{LockReleases: store, LockAuthority: authority}, authority: authority,
		requester: requester, holder: holder, repoID: repoID, path: "projekty/model.dwg", token: token,
	}
}

func lockReleaseTicket(t *testing.T, typ control.TicketType, clientID string, payload any) control.Ticket {
	t.Helper()
	ticket, err := control.NewTicket(uuid.NewString(), uuid.NewString(), typ, clientID, payload, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func decodeLockReleaseResult(t *testing.T, result control.Result) control.LockReleaseResult {
	t.Helper()
	var payload control.LockReleaseResult
	if err := control.DecodePayload(result.Result, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestWorkerLockReleaseRequestChecksLiveTokenAndIsIdempotent(t *testing.T) {
	fixture := newLockReleaseWorkerFixture(t)
	payload := control.RequestLockReleasePayload{RepoID: fixture.repoID, Path: fixture.path, ObservedLockID: fixture.token}
	first, err := fixture.worker.Handle(context.Background(), fixture.requester, lockReleaseTicket(t, control.TicketRequestLockRelease, fixture.requester.ClientID, payload))
	if err != nil || first.Status != control.ResultOK {
		t.Fatalf("first request = %+v err=%v", first, err)
	}
	firstRecord := decodeLockReleaseResult(t, first)
	second, err := fixture.worker.Handle(context.Background(), fixture.requester, lockReleaseTicket(t, control.TicketRequestLockRelease, fixture.requester.ClientID, payload))
	if err != nil || second.Status != control.ResultOK {
		t.Fatalf("retry = %+v err=%v", second, err)
	}
	secondRecord := decodeLockReleaseResult(t, second)
	if firstRecord.RequestID != secondRecord.RequestID || firstRecord.State != "pending" {
		t.Fatalf("idempotent records = %+v / %+v", firstRecord, secondRecord)
	}
	if fixture.authority.repoID != fixture.repoID || fixture.authority.path != fixture.path {
		t.Fatalf("authority scope = %q %q", fixture.authority.repoID, fixture.authority.path)
	}
}

func TestWorkerLockReleaseRejectsStaleObservationAndReadOnlyRequester(t *testing.T) {
	fixture := newLockReleaseWorkerFixture(t)
	payload := control.RequestLockReleasePayload{RepoID: fixture.repoID, Path: fixture.path, ObservedLockID: "opaquelocktoken:" + uuid.NewString()}
	result, err := fixture.worker.Handle(context.Background(), fixture.requester, lockReleaseTicket(t, control.TicketRequestLockRelease, fixture.requester.ClientID, payload))
	if err != nil || result.Status != control.ResultError || result.Error.Code != "LOCK_RELEASE_STALE" {
		t.Fatalf("stale result = %+v err=%v", result, err)
	}
	fixture.requester.Repositories[0].Access = "r"
	payload.ObservedLockID = fixture.token
	result, err = fixture.worker.Handle(context.Background(), fixture.requester, lockReleaseTicket(t, control.TicketRequestLockRelease, fixture.requester.ClientID, payload))
	if err != nil || result.Status != control.ResultError || result.Error.Code != "LOCK_RELEASE_FORBIDDEN" {
		t.Fatalf("read-only result = %+v err=%v", result, err)
	}
}

func TestWorkerLockReleaseOnlyCurrentHolderCanAccept(t *testing.T) {
	fixture := newLockReleaseWorkerFixture(t)
	requestResult, err := fixture.worker.Handle(context.Background(), fixture.requester, lockReleaseTicket(t, control.TicketRequestLockRelease, fixture.requester.ClientID, control.RequestLockReleasePayload{RepoID: fixture.repoID, Path: fixture.path, ObservedLockID: fixture.token}))
	if err != nil {
		t.Fatal(err)
	}
	request := decodeLockReleaseResult(t, requestResult)
	foreign := fixture.holder
	foreign.ClientID = uuid.NewString()
	result, err := fixture.worker.Handle(context.Background(), foreign, lockReleaseTicket(t, control.TicketAcceptLockRelease, foreign.ClientID, control.DecideLockReleasePayload{RequestID: request.RequestID}))
	if err != nil || result.Status != control.ResultError || result.Error.Code != "LOCK_RELEASE_FORBIDDEN" {
		t.Fatalf("foreign accept = %+v err=%v", result, err)
	}
	result, err = fixture.worker.Handle(context.Background(), fixture.holder, lockReleaseTicket(t, control.TicketAcceptLockRelease, fixture.holder.ClientID, control.DecideLockReleasePayload{RequestID: request.RequestID}))
	if err != nil || result.Status != control.ResultOK || decodeLockReleaseResult(t, result).State != "accepted" {
		t.Fatalf("holder accept = %+v err=%v", result, err)
	}
}

func TestWorkerLockReleaseReconcilesGoneLockWithoutInventingConsent(t *testing.T) {
	fixture := newLockReleaseWorkerFixture(t)
	requestResult, err := fixture.worker.Handle(context.Background(), fixture.requester, lockReleaseTicket(t, control.TicketRequestLockRelease, fixture.requester.ClientID, control.RequestLockReleasePayload{RepoID: fixture.repoID, Path: fixture.path, ObservedLockID: fixture.token}))
	if err != nil {
		t.Fatal(err)
	}
	request := decodeLockReleaseResult(t, requestResult)
	fixture.authority.observation = nil
	result, err := fixture.worker.Handle(context.Background(), fixture.holder, lockReleaseTicket(t, control.TicketDismissLockRelease, fixture.holder.ClientID, control.DecideLockReleasePayload{RequestID: request.RequestID}))
	if err != nil || result.Status != control.ResultOK || decodeLockReleaseResult(t, result).State != "lock_gone" {
		t.Fatalf("gone lock decision = %+v err=%v", result, err)
	}
}
