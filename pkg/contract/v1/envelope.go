// Package contract defines the versioned IPC contract between the FileES daemon
// and its clients (CLI, GUI). Clients may import this package freely; they must
// never import the engine packages (commit, watcher, client).
package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Protocol is the canonical protocol identifier sent in every envelope.
const Protocol = "filees.contract/v1"

// Status values for Response.Status.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Request is the envelope for all client-to-daemon commands.
type Request struct {
	Protocol  string          `json:"protocol"`
	RequestID string          `json:"request_id"`
	ClientID  string          `json:"client_id"`
	Command   string          `json:"command"`
	RepoID    string          `json:"repo_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// Validate checks the required envelope fields. Unknown fields in Payload are
// accepted without error (forward-compatibility rule §13).
func (r Request) Validate() error {
	if r.Protocol != Protocol {
		return errors.New("unsupported protocol: " + r.Protocol)
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return errors.New("missing request_id")
	}
	if strings.TrimSpace(r.ClientID) == "" {
		return errors.New("missing client_id")
	}
	if strings.TrimSpace(r.Command) == "" {
		return errors.New("missing command")
	}
	return nil
}

// Response is the envelope for all daemon-to-client replies.
type Response struct {
	Protocol  string          `json:"protocol"`
	RequestID string          `json:"request_id"`
	Status    string          `json:"status"`           // StatusOK | StatusError
	Result    json.RawMessage `json:"result,omitempty"` // present when Status == StatusOK
	Error     *ErrorBody      `json:"error,omitempty"`  // present when Status == StatusError
}

// ErrorBody is the structured error block inside an error Response.
// GUI translates MessageKey to a localised string; it must not parse Details text.
type ErrorBody struct {
	Code       string            `json:"code"`               // canonical errmap code, e.g. "LOCK-2001"
	Severity   string            `json:"severity"`           // INFO | WARN | ERROR | FATAL
	Hint       string            `json:"hint"`               // RETRY_LOCAL | RETRY_BACKOFF | REQUIRE_ACTION | ADMIN_ONLY
	MessageKey string            `json:"message_key"`        // i18n key, e.g. "lock.held_by_other"
	Details    map[string]string `json:"details,omitempty"`  // structured diagnostic context
}

// Event is the envelope for daemon-initiated state-change notifications.
// Clients must handle unknown event types gracefully (§13).
type Event struct {
	Protocol  string          `json:"protocol"`
	EventID   string          `json:"event_id"`            // stable UUID for this event instance
	Sequence  int64           `json:"sequence"`            // monotone within this daemon instance
	Timestamp string          `json:"timestamp"`           // RFC3339 UTC
	Type      string          `json:"type"`               // one of the Ev* constants
	RepoID    string          `json:"repo_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// --- builder helpers ---

// OKResponse wraps result (any JSON-serialisable value) in a success envelope.
func OKResponse(requestID string, result any) Response {
	b, _ := json.Marshal(result)
	return Response{Protocol: Protocol, RequestID: requestID, Status: StatusOK, Result: b}
}

// ErrResponse builds an error envelope from the given fields.
func ErrResponse(requestID, code, severity, hint, messageKey string, details map[string]string) Response {
	return Response{
		Protocol:  Protocol,
		RequestID: requestID,
		Status:    StatusError,
		Error: &ErrorBody{
			Code:       code,
			Severity:   severity,
			Hint:       hint,
			MessageKey: messageKey,
			Details:    details,
		},
	}
}

// NewEvent builds an outbound event envelope. The caller supplies eventID (UUID)
// and sequence — the daemon owns monotone counter and UUID generation.
func NewEvent(eventID string, seq int64, eventType, repoID string, payload any) Event {
	var raw json.RawMessage
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	return Event{
		Protocol:  Protocol,
		EventID:   eventID,
		Sequence:  seq,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Type:      eventType,
		RepoID:    repoID,
		Payload:   raw,
	}
}

// DecodePayload deserialises Request.Payload or Event.Payload into dst.
func DecodePayload(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// DecodeResult deserialises Response.Result into dst.
func DecodeResult(raw json.RawMessage, dst any) error {
	return DecodePayload(raw, dst)
}
