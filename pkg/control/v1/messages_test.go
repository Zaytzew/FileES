package v1

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func recoveryPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

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

func TestDeleteRepositoryContract(t *testing.T) {
	operationID, requestID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ticket, err := NewTicket(operationID, requestID, TicketDeleteRepository, "client-a", DeleteRepositoryPayload{RepoID: repoID}, time.Now())
	if err != nil || ticket.Type != TicketDeleteRepository {
		t.Fatalf("ticket=%+v err=%v", ticket, err)
	}
	retainUntil := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := NewSuccessResult(operationID, requestID, TicketDeleteRepository, DeleteRepositoryResult{RepoID: repoID, RetainUntil: retainUntil}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTicket(operationID, requestID, TicketDeleteRepository, "client-a", DeleteRepositoryPayload{RepoID: "../repo"}, time.Now()); err == nil {
		t.Fatal("traversing delete repo ID accepted")
	}
	if _, err := NewSuccessResult(operationID, requestID, TicketDeleteRepository, DeleteRepositoryResult{RepoID: repoID, RetainUntil: "never"}, time.Now()); err == nil {
		t.Fatal("invalid deletion retention timestamp accepted")
	}
}

// TestInitialCommitRepoIDMustBeUUID is the wire-level half of the audit's
// Finding B. INITIAL_COMMIT is the only ticket type whose repo_id travels back
// from the client, so it is the only place a client-chosen string can reach
// server-side path construction. It previously required nothing but
// non-emptiness while the sibling operation_id/request_id fields were both
// UUID-validated.
func TestInitialCommitRepoIDMustBeUUID(t *testing.T) {
	hostile := []string{
		"../../../../etc/passwd",
		"../activation",
		"foo/../../../bar",
		`..\windows`,
		"repo-a",
		"",
		"   ",
		"a\nb",
		"a\x00b",
		strings.Repeat("a", 4096),
	}
	for _, repoID := range hostile {
		ticket, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketInitialCommit, "client-a", InitialCommitPayload{RepoID: repoID, Revision: 1, Paths: 1}, time.Now())
		if err != nil {
			continue // rejected at construction time is equally fine
		}
		if err := ticket.Validate(); err == nil {
			t.Fatalf("INITIAL_COMMIT accepted repo_id %q", repoID)
		}
	}
	// A genuine, server-generated repo_id must still pass.
	ticket, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketInitialCommit, "client-a", InitialCommitPayload{RepoID: uuid.NewString(), Revision: 1, Paths: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := ticket.Validate(); err != nil {
		t.Fatalf("INITIAL_COMMIT rejected a valid UUID repo_id: %v", err)
	}
}

func TestRealmRemovalContractCarriesNoTargetScope(t *testing.T) {
	operationID, requestID := uuid.NewString(), uuid.NewString()
	ticket, err := NewTicket(operationID, requestID, TicketRealmRemoveRequest, "client-a", RealmRemoveRequestPayload{NotificationEmail: "user@example.net", ErasureRequested: true, RecoveryPublicKey: recoveryPublicKey(t)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ticket.Payload), "realm") || strings.Contains(string(ticket.Payload), "repo") || strings.Contains(string(ticket.Payload), "client") {
		t.Fatalf("realm-removal request exposed server-owned target: %s", ticket.Payload)
	}
	if _, err := NewSuccessResult(operationID, requestID, TicketRealmRemoveRequest, RealmRemoveRequestResult{ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), ActiveClientCount: 2, OwnedRepositoryCount: 3, ForeignGrantCount: 1, AdminContact: "admin@example.net"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTicket(operationID, uuid.NewString(), TicketRealmRemoveConfirm, "client-a", RealmRemoveConfirmPayload{OTP: "ABCDEFGH234567"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTicket(operationID, uuid.NewString(), TicketRealmRemoveConfirm, "client-a", RealmRemoveConfirmPayload{OTP: "bad-code"}, time.Now()); err == nil {
		t.Fatal("malformed realm removal OTP accepted")
	}
	created := time.Now().UTC()
	manifest := RealmRecoveryManifest{Schema: "filees.realm-recovery-manifest/v1", OperationID: operationID, RealmID: uuid.NewString(), CreatedAt: created, DownloadUntil: created.Add(time.Hour), AdminGraceUntil: created.Add(2 * time.Hour)}
	if _, err := NewSuccessResult(operationID, requestID, TicketRealmRemoveConfirm, RealmRemoveConfirmResult{State: "completed", Manifest: manifest, AdminContact: "admin@example.net"}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestRealmRemovalRequestRejectsForgedScope(t *testing.T) {
	ticket, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketRealmRemoveRequest, "client-a", RealmRemoveRequestPayload{NotificationEmail: "user@example.net", RecoveryPublicKey: recoveryPublicKey(t)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ticket.Payload = json.RawMessage(`{"notification_email":"user@example.net","realm_id":"` + uuid.NewString() + `"}`)
	if err := ticket.Validate(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("forged realm scope accepted: %v", err)
	}
}
