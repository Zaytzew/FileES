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

func TestLoadRepositoryDumpContract(t *testing.T) {
	operationID, requestID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ticket, err := NewTicket(operationID, requestID, TicketLoadRepositoryDump, "client-a", LoadRepositoryDumpPayload{RepoID: repoID}, time.Now())
	if err != nil || ticket.Type != TicketLoadRepositoryDump {
		t.Fatalf("ticket=%+v err=%v", ticket, err)
	}
	if strings.Contains(string(ticket.Payload), "revision") || strings.Contains(string(ticket.Payload), "path") {
		t.Fatalf("payload carries a client-supplied carrier path or revision: %s", ticket.Payload)
	}
	oldUUID, newUUID := uuid.NewString(), uuid.NewString()
	validResult := LoadRepositoryDumpResult{
		RepoID: repoID, OldUUID: oldUUID, NewUUID: newUUID,
		SourceRevisionRange: "r1:r842",
		ToolVersions:        map[string]string{"svnadmin": "1.14.5"},
	}
	if _, err := NewSuccessResult(operationID, requestID, TicketLoadRepositoryDump, validResult, time.Now()); err != nil {
		t.Fatal(err)
	}

	keep := 0
	if _, err := NewTicket(operationID, requestID, TicketLoadRepositoryDump, "client-a", LoadRepositoryDumpPayload{RepoID: repoID, KeepLastRevisions: &keep}, time.Now()); err == nil {
		t.Fatal("keep_last_revisions=0 accepted")
	}
	if _, err := NewTicket(operationID, requestID, TicketLoadRepositoryDump, "client-a", LoadRepositoryDumpPayload{RepoID: "../repo"}, time.Now()); err == nil {
		t.Fatal("non-UUID repo_id accepted")
	}
	sameUUID := validResult
	sameUUID.NewUUID = sameUUID.OldUUID
	if _, err := NewSuccessResult(operationID, requestID, TicketLoadRepositoryDump, sameUUID, time.Now()); err == nil {
		t.Fatal("result claiming an unchanged UUID (fake continuity) was accepted")
	}
	noTools := validResult
	noTools.ToolVersions = nil
	if _, err := NewSuccessResult(operationID, requestID, TicketLoadRepositoryDump, noTools, time.Now()); err == nil {
		t.Fatal("result without tool_versions accepted")
	}
}

func TestRealmGrantAndDirectoryContracts(t *testing.T) {
	op, request, client := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo, recipient := uuid.NewString(), uuid.NewString()
	for _, access := range []string{"r", "rw"} {
		if _, err := NewTicket(op, request, TicketGrantAccess, client, GrantAccessPayload{RepoID: repo, RecipientRealmID: recipient, Access: access}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSuccessResult(op, request, TicketGrantAccess, RealmGrantResult{RepoID: repo, RecipientRealmID: recipient, Access: access, State: "active"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewTicket(op, request, TicketGrantAccess, client, GrantAccessPayload{RepoID: repo, RecipientRealmID: recipient, Access: "admin"}, time.Now()); err == nil {
		t.Fatal("invalid grant access accepted")
	}
	if _, err := NewTicket(op, request, TicketRevokeAccess, client, RevokeAccessPayload{RepoID: repo, RecipientRealmID: recipient}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSuccessResult(op, request, TicketRevokeAccess, RealmGrantResult{RepoID: repo, RecipientRealmID: recipient, State: "revoked"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTicket(op, request, TicketListGrantRecipients, client, ListGrantRecipientsPayload{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSuccessResult(op, request, TicketListGrantRecipients, ListGrantRecipientsResult{Recipients: []GrantRecipient{{RealmID: recipient, Alias: "recipient"}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []string{"hidden", "listed"} {
		if _, err := NewTicket(op, request, TicketSetRealmVisibility, client, SetRealmDirectoryVisibilityPayload{Visibility: visibility}, time.Now()); err != nil {
			t.Fatal(err)
		}
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

func TestPublicShareTicketsContainNoOwnerOrPlaintextPassword(t *testing.T) {
	declaration := PublicShareDeclaration{RepoID: uuid.NewString(), SourceRoot: "wydanie", Slug: "przetarg-2026", PasswordHash: "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g", Objects: []PublicShareObject{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "wydanie/projekt.pdf", DisplayName: "Projekt.pdf"}}}
	operationID := uuid.NewString()
	ticket, err := NewTicket(operationID, uuid.NewString(), TicketCreatePublicShare, "client-a", CreatePublicSharePayload{PublicShareDeclaration: declaration}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ticket.Payload), "owner_realm") || strings.Contains(string(ticket.Payload), `"password"`) {
		t.Fatalf("public share ticket leaked owner/plain password: %s", ticket.Payload)
	}
	if _, err := NewSuccessResult(operationID, uuid.NewString(), TicketCreatePublicShare, PublicShareResult{ChannelID: operationID, Alias: "atmprojekt", Slug: declaration.Slug, State: "active"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	forged := ticket
	forged.Payload = json.RawMessage(`{"repo_id":"` + declaration.RepoID + `","source_root":"wydanie","slug":"przetarg-2026","owner_realm":"` + uuid.NewString() + `","object_map":[{"public_id":"7f3a1c9e2b4d6a80","repo_path":"wydanie/projekt.pdf","display_name":"Projekt.pdf"}]}`)
	if err := forged.Validate(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("payload-supplied owner accepted: %v", err)
	}
}

func TestPublicShareTicketRejectsPathOutsideSourceRoot(t *testing.T) {
	declaration := PublicShareDeclaration{RepoID: uuid.NewString(), SourceRoot: "wydanie", Slug: "przetarg-2026", Objects: []PublicShareObject{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "sekrety/projekt.pdf", DisplayName: "Projekt.pdf"}}}
	if _, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketCreatePublicShare, "client-a", CreatePublicSharePayload{PublicShareDeclaration: declaration}, time.Now()); err == nil {
		t.Fatal("object outside source_root accepted")
	}
}

func TestPublicShareTicketRejectsNonCanonicalRepoIDAndUnboundedPassword(t *testing.T) {
	declaration := PublicShareDeclaration{RepoID: strings.ToUpper(uuid.NewString()), SourceRoot: "wydanie", Slug: "przetarg-2026", Objects: []PublicShareObject{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "wydanie/projekt.pdf", DisplayName: "Projekt.pdf"}}}
	if _, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketCreatePublicShare, "client-a", CreatePublicSharePayload{PublicShareDeclaration: declaration}, time.Now()); err == nil {
		t.Fatal("non-canonical repository UUID was accepted")
	}
	declaration.RepoID = uuid.NewString()
	declaration.PasswordHash = "$argon2id$v=19$m=999999999,t=3,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketCreatePublicShare, "client-a", CreatePublicSharePayload{PublicShareDeclaration: declaration}, time.Now()); err == nil {
		t.Fatal("unbounded Argon2id verifier was accepted")
	}
}

func TestPublicShareListAndPasswordPreservationContracts(t *testing.T) {
	repoID, channelID := uuid.NewString(), uuid.NewString()
	if _, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketListPublicShares, "client-a", ListPublicSharesPayload{RepoID: repoID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	declaration := PublicShareDeclaration{RepoID: repoID, SourceRoot: "wydanie", Slug: "przetarg-2026", Objects: []PublicShareObject{{PublicID: "7f3a1c9e2b4d6a80", RepoPath: "wydanie/projekt.pdf", DisplayName: "Projekt.pdf"}}}
	if _, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketUpdatePublicShare, "client-a", UpdatePublicSharePayload{ChannelID: channelID, KeepPassword: true, PublicShareDeclaration: declaration}, time.Now()); err != nil {
		t.Fatal(err)
	}
	declaration.Recipients = []string{"a@example.com"}
	if _, err := NewTicket(uuid.NewString(), uuid.NewString(), TicketUpdatePublicShare, "client-a", UpdatePublicSharePayload{ChannelID: channelID, KeepPassword: true, PublicShareDeclaration: declaration}, time.Now()); err == nil {
		t.Fatal("password preservation with recipient tokens was accepted")
	}
	result := ListPublicSharesResult{Shares: []PublicShareSummary{{ChannelID: channelID, RepoID: repoID, Alias: "atmprojekt", Slug: "przetarg-2026", State: "active", SourceRoot: "wydanie", PasswordProtected: true, Objects: declaration.Objects, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}}
	if _, err := NewSuccessResult(uuid.NewString(), uuid.NewString(), TicketListPublicShares, result, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// A ticket type has to be registered in three independent places here -
// Ticket.Validate, the allowlist in Result.Validate, and
// validateSuccessPayload - and the default arms reject anything unregistered.
// Miss one and nothing fails to compile; it fails in production instead. This
// walks all three for the editing-policy ticket.
func TestSetRepositoryEditingPolicyIsRegisteredOnEveryValidationPath(t *testing.T) {
	opID, requestID, repoID := uuid.NewString(), uuid.NewString(), uuid.NewString()

	for _, policy := range []string{"", "free", "lock_required"} {
		if _, err := NewTicket(opID, requestID, TicketSetRepositoryEditingPolicy, "client", SetRepositoryEditingPolicyPayload{RepoID: repoID, Policy: policy}, time.Now()); err != nil {
			t.Fatalf("policy %q rejected: %v", policy, err)
		}
	}
	if _, err := NewTicket(opID, requestID, TicketSetRepositoryEditingPolicy, "client", SetRepositoryEditingPolicyPayload{RepoID: repoID, Policy: "readonly"}, time.Now()); err == nil {
		t.Fatal("unknown policy accepted into a ticket")
	}
	if _, err := NewTicket(opID, requestID, TicketSetRepositoryEditingPolicy, "client", SetRepositoryEditingPolicyPayload{RepoID: "not-a-uuid", Policy: "lock_required"}, time.Now()); err == nil {
		t.Fatal("non-UUID repo_id accepted into a ticket")
	}

	result, err := NewSuccessResult(opID, requestID, TicketSetRepositoryEditingPolicy, SetRepositoryEditingPolicyResult{RepoID: repoID, Policy: "lock_required"}, time.Now())
	if err != nil {
		t.Fatalf("result rejected by the type allowlist or payload validator: %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid result failed validation: %v", err)
	}
	// The stored default is the empty string, never the "free" alias, so a
	// result echoing "free" means a writer skipped normalisation.
	if _, err := NewSuccessResult(opID, requestID, TicketSetRepositoryEditingPolicy, SetRepositoryEditingPolicyResult{RepoID: repoID, Policy: "free"}, time.Now()); err == nil {
		t.Fatal("unnormalised \"free\" accepted in a result")
	}
}
