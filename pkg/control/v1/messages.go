// Package v1 defines the transport-neutral FileES control-plane protocol.
package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const Schema = "filees.control/v1"

type TicketType string

const (
	TicketCreateRepository TicketType = "CREATE_REPOSITORY"
	TicketInitialCommit    TicketType = "INITIAL_COMMIT"
	TicketStoragePreflight TicketType = "STORAGE_PREFLIGHT"
	TicketDeleteRepository TicketType = "DELETE_REPOSITORY"
	// TicketMobilePairing is requested by an already-active desktop client
	// to mint a mobile pairing token for its own realm (never a payload-
	// supplied one - the worker resolves realm_id from the authenticated
	// session, same discipline as every other ticket type here).
	TicketMobilePairing TicketType = "MOBILE_PAIRING"
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
	if r.Type != TicketStoragePreflight && r.Type != TicketCreateRepository && r.Type != TicketInitialCommit && r.Type != TicketDeleteRepository && r.Type != TicketMobilePairing {
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
