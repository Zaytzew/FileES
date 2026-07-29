// Package v1 defines the transport-neutral FileES control-plane protocol.
package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"filees/pkg/realmalias"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const Schema = "filees.control/v1"

type TicketType string

const (
	TicketCreateRepository   TicketType = "CREATE_REPOSITORY"
	TicketInitialCommit      TicketType = "INITIAL_COMMIT"
	TicketStoragePreflight   TicketType = "STORAGE_PREFLIGHT"
	TicketDeleteRepository   TicketType = "DELETE_REPOSITORY"
	TicketClaimRealmAlias    TicketType = "CLAIM_REALM_ALIAS"
	TicketResolveOwnerLabels TicketType = "RESOLVE_OWNER_LABELS"
	// TicketClientDeactivate lets an authenticated installation permanently remove
	// itself from a server. The server derives the target client from the SSH
	// session; the ticket carries no target ID beyond the required session
	// binding in Ticket.ClientID.
	TicketClientDeactivate TicketType = "CLIENT_DEACTIVATE"
	// TicketDetachClient is kept as a source-compatible alias for callers
	// compiled against the pre-rename API. Its wire value is CLIENT_DEACTIVATE.
	TicketDetachClient = TicketClientDeactivate
	// TicketMobilePairing is requested by an already-active desktop client
	// to mint a mobile pairing token for its own realm (never a payload-
	// supplied one - the worker resolves realm_id from the authenticated
	// session, same discipline as every other ticket type here).
	TicketMobilePairing TicketType = "MOBILE_PAIRING"
	// TicketRealmRemoveRequest starts the OTP-protected removal of the
	// authenticated realm's FileES participation. The payload intentionally
	// contains neither a realm ID nor any repository/client target.
	TicketRealmRemoveRequest TicketType = "REALM_REMOVE_REQUEST"
	// TicketRealmRemoveConfirm consumes the OTP for the same operation_id.
	// The server owns every destructive target from the request-time snapshot.
	TicketRealmRemoveConfirm TicketType = "REALM_REMOVE_CONFIRM"
)

type ResultStatus string

const (
	ResultOK    ResultStatus = "ok"
	ResultError ResultStatus = "error"
)

type Ticket struct {
	Schema      string          `json:"schema"`
	OperationID string          `json:"operation_id"`
	RequestID   string          `json:"request_id"`
	Type        TicketType      `json:"type"`
	ClientID    string          `json:"client_id"`
	CreatedAt   string          `json:"created_at"`
	Payload     json.RawMessage `json:"payload"`
}

type Result struct {
	Schema      string          `json:"schema"`
	OperationID string          `json:"operation_id"`
	RequestID   string          `json:"request_id"`
	Type        TicketType      `json:"type"`
	Status      ResultStatus    `json:"status"`
	CompletedAt string          `json:"completed_at"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       *ErrorBody      `json:"error,omitempty"`
}

type ErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}
type CreateRepositoryPayload struct {
	Name string `json:"name"`
}
type CreateRepositoryResult struct {
	RepoID  string `json:"repo_id"`
	RepoURL string `json:"repo_url"`
}
type InitialCommitPayload struct {
	RepoID   string `json:"repo_id"`
	Revision int64  `json:"revision"`
	Paths    int    `json:"paths"`
}
type InitialCommitResult struct {
	Acknowledged bool `json:"acknowledged"`
}
type DeleteRepositoryPayload struct {
	RepoID string `json:"repo_id"`
}
type DeleteRepositoryResult struct {
	RepoID      string `json:"repo_id"`
	RetainUntil string `json:"retain_until"`
}
type StoragePreflightPayload struct {
	ContentBytes int64 `json:"content_bytes"`
	Paths        int   `json:"paths"`
}
type StoragePreflightResult struct {
	AvailableBytes       int64  `json:"available_bytes"`
	RequiredBytes        int64  `json:"required_bytes"`
	ReservationExpiresAt string `json:"reservation_expires_at,omitempty"`
}

// MobilePairingPayload is deliberately empty: the requesting realm is
// derived from the authenticated session, never from the payload.
type MobilePairingPayload struct{}
type MobilePairingResult struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type ClaimRealmAliasPayload struct {
	Alias string `json:"alias"`
}

type ClaimRealmAliasResult struct {
	Alias string `json:"alias"`
}

type ResolveOwnerLabelsPayload struct {
	OwnerIDs []string `json:"owner_ids"`
}

type ResolveOwnerLabelsResult struct {
	Labels map[string]string `json:"labels"`
}

type ClientDeactivatePayload struct{}
type ClientDeactivateResult struct {
	ServiceRevision int64 `json:"service_revision"`
}
type DetachClientPayload = ClientDeactivatePayload
type DetachClientResult = ClientDeactivateResult

type RealmRemoveRequestPayload struct {
	NotificationEmail string `json:"notification_email"`
	ErasureRequested  bool   `json:"erasure_requested"`
	RecoveryPublicKey string `json:"recovery_public_key"`
}
type RealmRemoveRequestResult struct {
	ExpiresAt            string `json:"expires_at"`
	ActiveClientCount    int    `json:"active_client_count"`
	OwnedRepositoryCount int    `json:"owned_repository_count"`
	ForeignGrantCount    int    `json:"foreign_grant_count"`
}
type RealmRemoveConfirmPayload struct {
	OTP string `json:"otp"`
}
type RealmRemoveConfirmResult struct {
	State string `json:"state"`
}

func NewTicket(operationID, requestID string, typ TicketType, clientID string, payload any, now time.Time) (Ticket, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Ticket{}, fmt.Errorf("marshal control ticket payload: %w", err)
	}
	t := Ticket{Schema: Schema, OperationID: operationID, RequestID: requestID, Type: typ, ClientID: strings.TrimSpace(clientID), CreatedAt: now.UTC().Format(time.RFC3339Nano), Payload: raw}
	return t, t.Validate()
}

func NewSuccessResult(operationID, requestID string, typ TicketType, payload any, now time.Time) (Result, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("marshal control result payload: %w", err)
	}
	r := Result{Schema: Schema, OperationID: operationID, RequestID: requestID, Type: typ, Status: ResultOK, CompletedAt: now.UTC().Format(time.RFC3339Nano), Result: raw}
	return r, r.Validate()
}

func NewErrorResult(operationID, requestID string, typ TicketType, body ErrorBody, now time.Time) (Result, error) {
	r := Result{Schema: Schema, OperationID: operationID, RequestID: requestID, Type: typ, Status: ResultError, CompletedAt: now.UTC().Format(time.RFC3339Nano), Error: &body}
	return r, r.Validate()
}

func ParseTicket(raw []byte) (Ticket, error) {
	var ticket Ticket
	if err := decodeStrict(raw, &ticket); err != nil {
		return Ticket{}, err
	}
	return ticket, ticket.Validate()
}

func ParseResult(raw []byte) (Result, error) {
	var result Result
	if err := decodeStrict(raw, &result); err != nil {
		return Result{}, err
	}
	return result, result.Validate()
}

func (t Ticket) Validate() error {
	if t.Schema != Schema {
		return fmt.Errorf("unsupported control schema %q", t.Schema)
	}
	if err := validateUUID("operation_id", t.OperationID); err != nil {
		return err
	}
	if err := validateUUID("request_id", t.RequestID); err != nil {
		return err
	}
	if strings.TrimSpace(t.ClientID) == "" {
		return errors.New("client_id is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, t.CreatedAt); err != nil {
		return fmt.Errorf("invalid created_at: %w", err)
	}
	switch t.Type {
	case TicketStoragePreflight:
		var p StoragePreflightPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("STORAGE_PREFLIGHT payload: %w", err)
		}
		if p.ContentBytes < 0 || p.Paths < 0 {
			return errors.New("STORAGE_PREFLIGHT sizes cannot be negative")
		}
	case TicketCreateRepository:
		var p CreateRepositoryPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("CREATE_REPOSITORY payload: %w", err)
		}
		if strings.TrimSpace(p.Name) == "" {
			return errors.New("CREATE_REPOSITORY payload.name is required")
		}
	case TicketInitialCommit:
		var p InitialCommitPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("INITIAL_COMMIT payload: %w", err)
		}
		// INITIAL_COMMIT is the one ticket type where a previously-issued
		// repo_id travels back from the client, so it is the one place a
		// client-chosen string reaches server-side path construction. It must
		// be held to the same UUID shape as operation_id/request_id above; a
		// bare non-empty check would let "../.." through to filepath.Join.
		if err := validateUUID("INITIAL_COMMIT payload.repo_id", p.RepoID); err != nil {
			return err
		}
		if p.Revision < 0 {
			return errors.New("INITIAL_COMMIT payload.revision cannot be negative")
		}
		if p.Paths < 0 {
			return errors.New("INITIAL_COMMIT payload.paths cannot be negative")
		}
		if p.Revision == 0 && p.Paths != 0 {
			return errors.New("INITIAL_COMMIT revision zero requires an empty snapshot")
		}
	case TicketDeleteRepository:
		var p DeleteRepositoryPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("DELETE_REPOSITORY payload: %w", err)
		}
		if err := validateUUID("DELETE_REPOSITORY payload.repo_id", p.RepoID); err != nil {
			return err
		}
	case TicketMobilePairing:
		var p MobilePairingPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("MOBILE_PAIRING payload: %w", err)
		}
	case TicketClaimRealmAlias:
		var p ClaimRealmAliasPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("CLAIM_REALM_ALIAS payload: %w", err)
		}
		if _, err := realmalias.Normalize(p.Alias); err != nil {
			return fmt.Errorf("CLAIM_REALM_ALIAS alias: %w", err)
		}
	case TicketResolveOwnerLabels:
		var p ResolveOwnerLabelsPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("RESOLVE_OWNER_LABELS payload: %w", err)
		}
		if len(p.OwnerIDs) == 0 || len(p.OwnerIDs) > 128 {
			return errors.New("RESOLVE_OWNER_LABELS requires 1 to 128 owner IDs")
		}
		seen := make(map[string]struct{}, len(p.OwnerIDs))
		for _, id := range p.OwnerIDs {
			if err := validateUUID("RESOLVE_OWNER_LABELS owner_id", id); err != nil {
				return err
			}
			if _, duplicate := seen[id]; duplicate {
				return errors.New("RESOLVE_OWNER_LABELS contains duplicate owner ID")
			}
			seen[id] = struct{}{}
		}
	case TicketClientDeactivate:
		var p ClientDeactivatePayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("CLIENT_DEACTIVATE payload: %w", err)
		}
	case TicketRealmRemoveRequest:
		var p RealmRemoveRequestPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("REALM_REMOVE_REQUEST payload: %w", err)
		}
		if _, err := validatePlainEmail(p.NotificationEmail); err != nil {
			return fmt.Errorf("REALM_REMOVE_REQUEST notification_email: %w", err)
		}
		if err := validateRecoveryPublicKey(p.RecoveryPublicKey); err != nil {
			return fmt.Errorf("REALM_REMOVE_REQUEST recovery_public_key: %w", err)
		}
	case TicketRealmRemoveConfirm:
		var p RealmRemoveConfirmPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("REALM_REMOVE_CONFIRM payload: %w", err)
		}
		if !validRealmRemovalOTP(p.OTP) {
			return errors.New("REALM_REMOVE_CONFIRM otp is invalid")
		}
	default:
		return fmt.Errorf("unsupported ticket type %q", t.Type)
	}
	return nil
}

func (r Result) Validate() error {
	if r.Schema != Schema {
		return fmt.Errorf("unsupported control schema %q", r.Schema)
	}
	if err := validateUUID("operation_id", r.OperationID); err != nil {
		return err
	}
	if err := validateUUID("request_id", r.RequestID); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, r.CompletedAt); err != nil {
		return fmt.Errorf("invalid completed_at: %w", err)
	}
	if r.Type != TicketStoragePreflight && r.Type != TicketCreateRepository && r.Type != TicketInitialCommit && r.Type != TicketDeleteRepository && r.Type != TicketMobilePairing && r.Type != TicketClaimRealmAlias && r.Type != TicketResolveOwnerLabels && r.Type != TicketClientDeactivate && r.Type != TicketRealmRemoveRequest && r.Type != TicketRealmRemoveConfirm {
		return fmt.Errorf("unsupported ticket type %q", r.Type)
	}
	switch r.Status {
	case ResultOK:
		if r.Error != nil {
			return errors.New("successful result cannot contain error")
		}
		return validateSuccessPayload(r)
	case ResultError:
		if r.Error == nil || strings.TrimSpace(r.Error.Code) == "" || strings.TrimSpace(r.Error.Message) == "" {
			return errors.New("error result requires error code and message")
		}
		if len(r.Result) != 0 && string(r.Result) != "null" {
			return errors.New("error result cannot contain success payload")
		}
		return nil
	default:
		return fmt.Errorf("unsupported result status %q", r.Status)
	}
}

func validateSuccessPayload(r Result) error {
	switch r.Type {
	case TicketStoragePreflight:
		var result StoragePreflightResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("STORAGE_PREFLIGHT result: %w", err)
		}
		if result.AvailableBytes < 0 || result.RequiredBytes < 0 {
			return errors.New("STORAGE_PREFLIGHT result sizes cannot be negative")
		}
		if result.ReservationExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, result.ReservationExpiresAt); err != nil {
				return fmt.Errorf("invalid STORAGE_PREFLIGHT reservation expiry: %w", err)
			}
		}
	case TicketCreateRepository:
		var result CreateRepositoryResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("CREATE_REPOSITORY result: %w", err)
		}
		if strings.TrimSpace(result.RepoID) == "" || strings.TrimSpace(result.RepoURL) == "" {
			return errors.New("CREATE_REPOSITORY result requires repo_id and repo_url")
		}
	case TicketInitialCommit:
		var result InitialCommitResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("INITIAL_COMMIT result: %w", err)
		}
		if !result.Acknowledged {
			return errors.New("INITIAL_COMMIT result must be acknowledged")
		}
	case TicketDeleteRepository:
		var result DeleteRepositoryResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("DELETE_REPOSITORY result: %w", err)
		}
		if err := validateUUID("DELETE_REPOSITORY result.repo_id", result.RepoID); err != nil {
			return err
		}
		if _, err := time.Parse(time.RFC3339Nano, result.RetainUntil); err != nil {
			return fmt.Errorf("invalid DELETE_REPOSITORY retain_until: %w", err)
		}
	case TicketMobilePairing:
		var result MobilePairingResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("MOBILE_PAIRING result: %w", err)
		}
		if strings.TrimSpace(result.Token) == "" {
			return errors.New("MOBILE_PAIRING result requires token")
		}
		if _, err := time.Parse(time.RFC3339Nano, result.ExpiresAt); err != nil {
			return fmt.Errorf("invalid MOBILE_PAIRING expires_at: %w", err)
		}
	case TicketClaimRealmAlias:
		var result ClaimRealmAliasResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("CLAIM_REALM_ALIAS result: %w", err)
		}
		if _, err := realmalias.Normalize(result.Alias); err != nil {
			return fmt.Errorf("CLAIM_REALM_ALIAS result alias: %w", err)
		}
	case TicketResolveOwnerLabels:
		var result ResolveOwnerLabelsResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("RESOLVE_OWNER_LABELS result: %w", err)
		}
		if len(result.Labels) > 128 {
			return errors.New("RESOLVE_OWNER_LABELS result contains too many labels")
		}
		for id, label := range result.Labels {
			if err := validateUUID("RESOLVE_OWNER_LABELS result owner_id", id); err != nil {
				return err
			}
			if _, err := realmalias.Normalize(label); err != nil {
				return fmt.Errorf("RESOLVE_OWNER_LABELS result label: %w", err)
			}
		}
	case TicketClientDeactivate:
		var result ClientDeactivateResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("CLIENT_DEACTIVATE result: %w", err)
		}
		if result.ServiceRevision < 1 {
			return errors.New("CLIENT_DEACTIVATE result service revision must be positive")
		}
	case TicketRealmRemoveRequest:
		var result RealmRemoveRequestResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("REALM_REMOVE_REQUEST result: %w", err)
		}
		if _, err := time.Parse(time.RFC3339Nano, result.ExpiresAt); err != nil {
			return fmt.Errorf("invalid REALM_REMOVE_REQUEST expiry: %w", err)
		}
		if result.ActiveClientCount < 0 || result.OwnedRepositoryCount < 0 || result.ForeignGrantCount < 0 {
			return errors.New("REALM_REMOVE_REQUEST result counts cannot be negative")
		}
	case TicketRealmRemoveConfirm:
		var result RealmRemoveConfirmResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("REALM_REMOVE_CONFIRM result: %w", err)
		}
		if result.State != "deleting" && result.State != "recovery_ready" && result.State != "revoke_all_clients" && result.State != "completed" {
			return errors.New("REALM_REMOVE_CONFIRM result state is invalid")
		}
	}
	return nil
}

func DecodePayload(raw json.RawMessage, dst any) error       { return decodeStrict(raw, dst) }
func DecodeResultPayload(raw json.RawMessage, dst any) error { return decodeStrict(raw, dst) }

func decodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return errors.New("payload is required")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func validateUUID(field, value string) error {
	if _, err := uuid.Parse(value); err != nil {
		return fmt.Errorf("%s must be UUID: %w", field, err)
	}
	return nil
}

func validatePlainEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 254 || strings.ContainsAny(value, "<>\r\n\t ") {
		return "", errors.New("must be a plain mailbox address")
	}
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || at == len(value)-1 || strings.Count(value, "@") != 1 {
		return "", errors.New("must be a plain mailbox address")
	}
	return value, nil
}

func validRealmRemovalOTP(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 12 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z') && !(r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}

func validateRecoveryPublicKey(value string) error {
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(value)))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || comment != "" || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return errors.New("must be one comment-free Ed25519 public key")
	}
	return nil
}
