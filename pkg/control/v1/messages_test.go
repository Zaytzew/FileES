package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCreateRepositoryTicketRoundTripAndValidate(t *testing.T) {
	ticket, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketCreateRepository, "client-a", CreateRepositoryPayload{Name: "Project A"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ticket)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Ticket
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTicketValidationRejectsUnknownPayloadField(t *testing.T) {
	ticket, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketCreateRepository, "client-a", CreateRepositoryPayload{Name: "Project A"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ticket.Payload = json.RawMessage(`{"name":"Project A","owner":"forged"}`)
	if err := ticket.Validate(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Validate() = %v, want unknown field", err)
	}
}

func TestResultValidationEnforcesStatusPayload(t *testing.T) {
	raw, _ := json.Marshal(CreateRepositoryResult{RepoID: "repo-a", RepoURL: "svn://example/repo-a"})
	r := Result{Schema: Schema, OperationID: uuid.NewString(), RequestID: uuid.NewString(), Type: TicketCreateRepository, Status: ResultOK, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: raw}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Error = &ErrorBody{Code: "FORGED", Message: "invalid"}
	if err := r.Validate(); err == nil {
		t.Fatal("successful result accepted error body")
	}
}

func TestTicketValidationRejectsInvalidIDs(t *testing.T) {
	ticket := Ticket{Schema: Schema, OperationID: "bad", RequestID: uuid.NewString(), Type: TicketCreateRepository, ClientID: "client", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Payload: json.RawMessage(`{"name":"x"}`)}
	if err := ticket.Validate(); err == nil {
		t.Fatal("invalid operation ID accepted")
	}
}

func TestParseTicketRejectsUnknownEnvelopeField(t *testing.T) {
	raw := []byte(`{"schema":"filees.control/v1","operation_id":"` + uuid.NewString() + `","request_id":"` + uuid.NewString() + `","type":"CREATE_REPOSITORY","client_id":"client","created_at":"2026-07-15T00:00:00Z","payload":{"name":"x"},"owner":"forged"}`)
	if _, err := ParseTicket(raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("ParseTicket() = %v, want unknown field", err)
	}
}

func TestResultConstructors(t *testing.T) {
	opID, reqID := uuid.NewString(), uuid.NewString()
	if _, err := NewSuccessResult(opID, reqID, TicketInitialCommit, InitialCommitResult{Acknowledged: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewErrorResult(opID, reqID, TicketInitialCommit, ErrorBody{Code: "FAILED", Message: "failed"}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestStoragePreflightContract(t *testing.T) {
	opID, requestID := uuid.NewString(), uuid.NewString()
	if _, err := NewTicket(opID, requestID, TicketStoragePreflight, "client", StoragePreflightPayload{ContentBytes: 123, Paths: 4}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSuccessResult(opID, requestID, TicketStoragePreflight, StoragePreflightResult{AvailableBytes: 1000, RequiredBytes: 310}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTicket(opID, requestID, TicketStoragePreflight, "client", StoragePreflightPayload{ContentBytes: -1}, time.Now()); err == nil {
		t.Fatal("negative content size accepted")
	}
}

func TestMobilePairingContract(t *testing.T) {
	opID, requestID := uuid.NewString(), uuid.NewString()
	ticket, err := NewTicket(opID, requestID, TicketMobilePairing, "client", MobilePairingPayload{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The payload deliberately carries no realm_id - the worker resolves it
	// from the authenticated session, never from a client-supplied field.
	if ticket.Type != TicketMobilePairing || string(ticket.Payload) != "{}" {
		t.Fatalf("ticket=%+v, want empty MOBILE_PAIRING payload", ticket)
	}
	if _, err := NewSuccessResult(opID, requestID, TicketMobilePairing, MobilePairingResult{Token: "AAAAAAAA-BBBBBBBBBBBBBBBB", ExpiresAt: time.Now().Format(time.RFC3339Nano)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSuccessResult(opID, requestID, TicketMobilePairing, MobilePairingResult{Token: "", ExpiresAt: time.Now().Format(time.RFC3339Nano)}, time.Now()); err == nil {
		t.Fatal("empty token accepted")
	}
	if _, err := NewSuccessResult(opID, requestID, TicketMobilePairing, MobilePairingResult{Token: "x", ExpiresAt: "not-a-time"}, time.Now()); err == nil {
		t.Fatal("invalid expires_at accepted")
	}
}
