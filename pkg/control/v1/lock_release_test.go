package v1

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLockReleaseTicketsAndResultsValidate(t *testing.T) {
	now := time.Now().UTC()
	repoID := uuid.NewString()
	requestID := uuid.NewString()
	token := "opaquelocktoken:" + uuid.NewString()
	for _, tc := range []struct {
		typ     TicketType
		payload any
	}{
		{TicketRequestLockRelease, RequestLockReleasePayload{RepoID: repoID, Path: "projekty/model.dwg", ObservedLockID: token}},
		{TicketDismissLockRelease, DecideLockReleasePayload{RequestID: requestID}},
		{TicketAcceptLockRelease, DecideLockReleasePayload{RequestID: requestID}},
	} {
		ticket, err := NewTicket(uuid.NewString(), uuid.NewString(), tc.typ, uuid.NewString(), tc.payload, now)
		if err != nil {
			t.Fatalf("%s ticket: %v", tc.typ, err)
		}
		result, err := NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, LockReleaseResult{
			RequestID: requestID, RepoID: repoID, Path: "projekty/model.dwg", ObservedLockID: token,
			State: "pending", ExpiresAt: now.Add(3 * time.Hour).Format(time.RFC3339Nano),
		}, now)
		if err != nil || result.Status != ResultOK {
			t.Fatalf("%s result = %+v err=%v", tc.typ, result, err)
		}
	}
}

func TestLockReleaseContractRejectsPathsTokensAndStates(t *testing.T) {
	now := time.Now().UTC()
	clientID := uuid.NewString()
	repoID := uuid.NewString()
	for name, payload := range map[string]RequestLockReleasePayload{
		"path":  {RepoID: repoID, Path: "../secret", ObservedLockID: "opaquelocktoken:" + uuid.NewString()},
		"token": {RepoID: repoID, Path: "file.txt", ObservedLockID: "bad\nvalue"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketRequestLockRelease, clientID, payload, now); err == nil {
				t.Fatal("invalid request ticket accepted")
			}
		})
	}
	if _, err := NewSuccessResult(uuid.NewString(), uuid.NewString(), TicketRequestLockRelease, LockReleaseResult{
		RequestID: uuid.NewString(), RepoID: repoID, Path: "file.txt", ObservedLockID: "opaquelocktoken:" + uuid.NewString(),
		State: "chatting", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano),
	}, now); err == nil {
		t.Fatal("invalid lock release result state accepted")
	}
}
