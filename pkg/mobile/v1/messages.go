// Package v1 defines the transport-neutral FileES mobile protocol.
//
// The mobile client is read-only for existing objects and append-only-unique for
// new ones: it never modifies or deletes an existing path. Every operation is one
// framed request over a single SSH session (see framing.go); the worker is the
// sole authority for grants, uniqueness and svn:needs-lock. This package carries
// only the wire contract and its validation — no SSH, no SVN, no Android.
package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	// Schema tags request and response envelopes.
	Schema = "filees.mobile/v1"
	// ManifestSchema tags the manifest projection (also the client cache artifact).
	ManifestSchema = "filees.mobile-manifest/v2"
)

// Operation is the mobile verb carried in a request envelope.
type Operation string

const (
	OpRefreshManifest Operation = "REFRESH_MANIFEST"
	OpListDirectory   Operation = "LIST_DIRECTORY"
	OpReadObject      Operation = "READ_OBJECT"
	OpUploadObject    Operation = "UPLOAD_OBJECT" // append-only, unique
	OpOperationStatus Operation = "GET_OPERATION_STATUS"
)

func (o Operation) valid() bool {
	switch o {
	case OpRefreshManifest, OpListDirectory, OpReadObject, OpUploadObject, OpOperationStatus:
		return true
	}
	return false
}

// Status is the transport-level result of a request. Domain outcomes that are not
// failures (a name collision, a revoked grant) travel inside an OK result as an
// Outcome — never as raw SVN text.
type Status string

const (
	StatusOK    Status = "ok"
	StatusError Status = "error"
)

// Outcome is the domain result of an UPLOAD_OBJECT append.
type Outcome string

const (
	OutcomeCommitted     Outcome = "COMMITTED"        // new unique object published
	OutcomeNameTakenSame Outcome = "NAME_TAKEN_SAME"  // path exists, identical content -> drop
	OutcomeNameTakenDiff Outcome = "NAME_TAKEN_DIFF"  // path exists, different content -> user decides
	OutcomeDestGone      Outcome = "DESTINATION_GONE" // parent directory vanished
	OutcomeAccessRevoked Outcome = "ACCESS_REVOKED"   // installation lost rw
	OutcomeRepoInactive  Outcome = "REPO_INACTIVE"
	OutcomePolicyReject  Outcome = "POLICY_REJECTED"
)

func (o Outcome) valid() bool {
	switch o {
	case OutcomeCommitted, OutcomeNameTakenSame, OutcomeNameTakenDiff,
		OutcomeDestGone, OutcomeAccessRevoked, OutcomeRepoInactive, OutcomePolicyReject:
		return true
	}
	return false
}

// OpState is the durable operation-status reported by GET_OPERATION_STATUS,
// sourced from the worker ledger (revprop is only its recovery anchor).
type OpState string

const (
	OpStateUnknown    OpState = "UNKNOWN"
	OpStateReceiving  OpState = "RECEIVING"
	OpStateValidating OpState = "VALIDATING"
	OpStateCommitting OpState = "COMMITTING"
	OpStateCommitted  OpState = "COMMITTED"
	OpStateRejected   OpState = "REJECTED"
)

// Kind is a manifest entry kind.
type Kind string

const (
	KindFile      Kind = "file"
	KindDirectory Kind = "directory"
)

// Request is the framed request envelope (the frame header JSON).
type Request struct {
	Schema    string          `json:"schema"`
	RequestID string          `json:"request_id"`
	Operation Operation       `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
}

// Response is the framed response envelope. On StatusOK, Result carries the
// operation result; on StatusError, Error carries a domain code and message.
type Response struct {
	Schema    string          `json:"schema"`
	RequestID string          `json:"request_id"`
	Operation Operation       `json:"operation"`
	Status    Status          `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *ErrorBody      `json:"error,omitempty"`
}

// ErrorBody is a transport/validation failure. It never carries raw tool output.
type ErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// --- operation payloads ---

type RefreshManifestPayload struct {
	RepoID              string `json:"repo_id"`
	KnownViewGeneration int64  `json:"known_view_generation"`
	KnownRepoRevision   int64  `json:"known_repo_revision"`
}

// RefreshManifestResult is NOT_MODIFIED (Manifest nil) only when both the view
// generation and the repo revision are unchanged; otherwise Manifest is present.
type RefreshManifestResult struct {
	NotModified bool      `json:"not_modified"`
	Manifest    *Manifest `json:"manifest,omitempty"`
}

type ListDirectoryPayload struct {
	RepoID string `json:"repo_id"`
	Path   string `json:"path"`
}

type ReadObjectPayload struct {
	RepoID string `json:"repo_id"`
	Path   string `json:"path"`
}

// ReadObjectResult heads a READ_OBJECT response; content streams as frame payload.
type ReadObjectResult struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Sha256      string `json:"sha256,omitempty"`
}

// UploadObjectPayload heads an append; content streams as frame payload.
// ObservedRevision is audit/UX context only — never a commit gate.
type UploadObjectPayload struct {
	RepoID           string `json:"repo_id"`
	ParentPath       string `json:"parent_path"`
	Filename         string `json:"filename"`
	Size             int64  `json:"size"`
	Sha256           string `json:"sha256"`
	ContentType      string `json:"content_type,omitempty"`
	ObservedRevision int64  `json:"observed_revision,omitempty"`
}

// UploadObjectResult carries the append outcome. Revision/FinalPath are set on
// COMMITTED; ExistingSha256 is set on a NAME_TAKEN_* collision.
type UploadObjectResult struct {
	Outcome        Outcome `json:"outcome"`
	Revision       int64   `json:"revision,omitempty"`
	FinalPath      string  `json:"final_path,omitempty"`
	ExistingSha256 string  `json:"existing_sha256,omitempty"`
}

type OperationStatusPayload struct {
	TargetRequestID string `json:"target_request_id"`
}

type OperationStatusResult struct {
	State    OpState `json:"state"`
	Revision int64   `json:"revision,omitempty"`
}

// --- manifest ---

// Manifest is the authoritative, monotonic projection. Freshness has two
// dimensions: RepoRevision (SVN commit) and ViewGeneration (control-plane: grants,
// role, policy, shouts, visibility — changing without a commit).
type Manifest struct {
	Schema         string          `json:"schema"`
	RepoID         string          `json:"repo_id"`
	ViewGeneration int64           `json:"view_generation"`
	RepoRevision   int64           `json:"repo_revision"`
	Complete       bool            `json:"complete"`
	Entries        []ManifestEntry `json:"entries"`
}

// ManifestEntry is one listing row. ContentHash is optional (nullable): svn list
// gives no SHA-256, so hashing every path would turn REFRESH_MANIFEST into a
// backup. It is populated only when cheap or already known, or computed on demand
// at a name collision.
type ManifestEntry struct {
	Path                string  `json:"path"`
	Kind                Kind    `json:"kind"`
	Size                int64   `json:"size,omitempty"`
	LastChangedRevision int64   `json:"last_changed_revision"`
	ContentHash         *string `json:"content_hash"`
}

// --- constructors ---

func NewRequest(requestID string, op Operation, payload any) (Request, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Request{}, fmt.Errorf("marshal mobile request payload: %w", err)
	}
	r := Request{Schema: Schema, RequestID: requestID, Operation: op, Payload: raw}
	return r, r.Validate()
}

func NewSuccess(requestID string, op Operation, result any) (Response, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return Response{}, fmt.Errorf("marshal mobile result: %w", err)
	}
	r := Response{Schema: Schema, RequestID: requestID, Operation: op, Status: StatusOK, Result: raw}
	return r, r.Validate()
}

func NewError(requestID string, op Operation, body ErrorBody) (Response, error) {
	r := Response{Schema: Schema, RequestID: requestID, Operation: op, Status: StatusError, Error: &body}
	return r, r.Validate()
}

func ParseRequest(raw []byte) (Request, error) {
	var req Request
	if err := decodeStrict(raw, &req); err != nil {
		return Request{}, err
	}
	return req, req.Validate()
}

func ParseResponse(raw []byte) (Response, error) {
	var resp Response
	if err := decodeStrict(raw, &resp); err != nil {
		return Response{}, err
	}
	return resp, resp.Validate()
}

// --- validation ---

func (r Request) Validate() error {
	if r.Schema != Schema {
		return fmt.Errorf("unsupported mobile schema %q", r.Schema)
	}
	if err := validateUUID("request_id", r.RequestID); err != nil {
		return err
	}
	if !r.Operation.valid() {
		return fmt.Errorf("unsupported operation %q", r.Operation)
	}
	switch r.Operation {
	case OpRefreshManifest:
		var p RefreshManifestPayload
		if err := decodeStrict(r.Payload, &p); err != nil {
			return fmt.Errorf("%s payload: %w", r.Operation, err)
		}
		if err := requireRepoID(p.RepoID); err != nil {
			return err
		}
		if p.KnownViewGeneration < 0 || p.KnownRepoRevision < 0 {
			return errors.New("known_view_generation and known_repo_revision cannot be negative")
		}
	case OpListDirectory:
		var p ListDirectoryPayload
		if err := decodeStrict(r.Payload, &p); err != nil {
			return fmt.Errorf("%s payload: %w", r.Operation, err)
		}
		if err := requireRepoID(p.RepoID); err != nil {
			return err
		}
		if err := validateRelPath("path", p.Path, true); err != nil {
			return err
		}
	case OpReadObject:
		var p ReadObjectPayload
		if err := decodeStrict(r.Payload, &p); err != nil {
			return fmt.Errorf("%s payload: %w", r.Operation, err)
		}
		if err := requireRepoID(p.RepoID); err != nil {
			return err
		}
		if err := validateRelPath("path", p.Path, false); err != nil {
			return err
		}
	case OpUploadObject:
		var p UploadObjectPayload
		if err := decodeStrict(r.Payload, &p); err != nil {
			return fmt.Errorf("%s payload: %w", r.Operation, err)
		}
		if err := requireRepoID(p.RepoID); err != nil {
			return err
		}
		if err := validateRelPath("parent_path", p.ParentPath, true); err != nil {
			return err
		}
		if err := validateFilename(p.Filename); err != nil {
			return err
		}
		if p.Size < 0 {
			return errors.New("upload size cannot be negative")
		}
		if err := validateSha256Hex(p.Sha256); err != nil {
			return err
		}
		if p.ObservedRevision < 0 {
			return errors.New("observed_revision cannot be negative")
		}
	case OpOperationStatus:
		var p OperationStatusPayload
		if err := decodeStrict(r.Payload, &p); err != nil {
			return fmt.Errorf("%s payload: %w", r.Operation, err)
		}
		if err := validateUUID("target_request_id", p.TargetRequestID); err != nil {
			return err
		}
	}
	return nil
}

func (r Response) Validate() error {
	if r.Schema != Schema {
		return fmt.Errorf("unsupported mobile schema %q", r.Schema)
	}
	if err := validateUUID("request_id", r.RequestID); err != nil {
		return err
	}
	if !r.Operation.valid() {
		return fmt.Errorf("unsupported operation %q", r.Operation)
	}
	switch r.Status {
	case StatusError:
		if r.Error == nil || strings.TrimSpace(r.Error.Code) == "" || strings.TrimSpace(r.Error.Message) == "" {
			return errors.New("error response requires error code and message")
		}
		if len(r.Result) != 0 && string(r.Result) != "null" {
			return errors.New("error response cannot carry a success result")
		}
		return nil
	case StatusOK:
		if r.Error != nil {
			return errors.New("ok response cannot carry an error")
		}
		return r.validateResult()
	default:
		return fmt.Errorf("unsupported status %q", r.Status)
	}
}

func (r Response) validateResult() error {
	switch r.Operation {
	case OpRefreshManifest:
		var res RefreshManifestResult
		if err := decodeStrict(r.Result, &res); err != nil {
			return fmt.Errorf("%s result: %w", r.Operation, err)
		}
		if res.NotModified {
			if res.Manifest != nil {
				return errors.New("not_modified result cannot carry a manifest")
			}
			return nil
		}
		if res.Manifest == nil {
			return errors.New("refresh result requires a manifest unless not_modified")
		}
		return res.Manifest.Validate()
	case OpReadObject:
		var res ReadObjectResult
		if err := decodeStrict(r.Result, &res); err != nil {
			return fmt.Errorf("%s result: %w", r.Operation, err)
		}
		if err := validateRelPath("path", res.Path, false); err != nil {
			return err
		}
		if res.Size < 0 {
			return errors.New("read result size cannot be negative")
		}
		if res.Sha256 != "" {
			if err := validateSha256Hex(res.Sha256); err != nil {
				return err
			}
		}
	case OpUploadObject:
		var res UploadObjectResult
		if err := decodeStrict(r.Result, &res); err != nil {
			return fmt.Errorf("%s result: %w", r.Operation, err)
		}
		if !res.Outcome.valid() {
			return fmt.Errorf("invalid upload outcome %q", res.Outcome)
		}
		switch res.Outcome {
		case OutcomeCommitted:
			if res.Revision <= 0 || res.FinalPath == "" {
				return errors.New("committed upload requires revision and final_path")
			}
		case OutcomeNameTakenSame, OutcomeNameTakenDiff:
			if res.ExistingSha256 != "" {
				if err := validateSha256Hex(res.ExistingSha256); err != nil {
					return err
				}
			}
		}
	case OpListDirectory:
		var res Manifest
		if err := decodeStrict(r.Result, &res); err != nil {
			return fmt.Errorf("%s result: %w", r.Operation, err)
		}
		return res.Validate()
	case OpOperationStatus:
		var res OperationStatusResult
		if err := decodeStrict(r.Result, &res); err != nil {
			return fmt.Errorf("%s result: %w", r.Operation, err)
		}
		if !validOpState(res.State) {
			return fmt.Errorf("invalid operation state %q", res.State)
		}
	}
	return nil
}

func (m Manifest) Validate() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	if err := requireRepoID(m.RepoID); err != nil {
		return err
	}
	if m.ViewGeneration < 1 {
		return errors.New("manifest view_generation must be positive")
	}
	if m.RepoRevision < 0 {
		return errors.New("manifest repo_revision cannot be negative")
	}
	seen := make(map[string]struct{}, len(m.Entries))
	for i, e := range m.Entries {
		if err := validateRelPath(fmt.Sprintf("entries[%d].path", i), e.Path, false); err != nil {
			return err
		}
		if _, dup := seen[e.Path]; dup {
			return fmt.Errorf("entries[%d].path %q is duplicated", i, e.Path)
		}
		seen[e.Path] = struct{}{}
		if e.Kind != KindFile && e.Kind != KindDirectory {
			return fmt.Errorf("entries[%d].kind %q is invalid", i, e.Kind)
		}
		if e.Size < 0 {
			return fmt.Errorf("entries[%d].size cannot be negative", i)
		}
		if e.LastChangedRevision < 0 {
			return fmt.Errorf("entries[%d].last_changed_revision cannot be negative", i)
		}
		if e.ContentHash != nil {
			if err := validateContentHash(*e.ContentHash); err != nil {
				return fmt.Errorf("entries[%d].content_hash: %w", i, err)
			}
		}
	}
	return nil
}

// --- helpers ---

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func validateUUID(field, value string) error {
	if _, err := uuid.Parse(value); err != nil {
		return fmt.Errorf("%s must be a UUID", field)
	}
	return nil
}

func requireRepoID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "\x00\r\n\t/\\") {
		return errors.New("repo_id is invalid")
	}
	return nil
}

// validateRelPath enforces the traversal-safe logical path rules. NFC
// canonicalization is applied at the worker boundary, not here.
func validateRelPath(field, p string, allowEmpty bool) error {
	if p == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%s is required", field)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s must be relative", field)
	}
	if strings.ContainsAny(p, "\x00\\") || hasControl(p) {
		return fmt.Errorf("%s contains invalid characters", field)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			return fmt.Errorf("%s has an empty segment", field)
		}
		if seg == "." || seg == ".." {
			return fmt.Errorf("%s must not contain . or ..", field)
		}
	}
	return nil
}

func validateFilename(name string) error {
	if name == "" {
		return errors.New("filename is required")
	}
	if name == "." || name == ".." {
		return errors.New("filename must not be . or ..")
	}
	if strings.ContainsAny(name, "\x00/\\") || hasControl(name) {
		return errors.New("filename contains invalid characters")
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 {
			return true
		}
	}
	return false
}

func validateSha256Hex(h string) error {
	if len(h) != 64 || !isLowerHex(h) {
		return errors.New("sha256 must be 64 lowercase hex characters")
	}
	return nil
}

// validateContentHash accepts an algorithm-prefixed digest, e.g. "sha256:<hex>"
// or "sha1:<hex>" (FSFS native checksum for cheap on-demand collision checks).
func validateContentHash(h string) error {
	algo, hex, ok := strings.Cut(h, ":")
	if !ok || hex == "" || !isLowerHex(hex) {
		return errors.New("content_hash must be <algo>:<hex>")
	}
	switch algo {
	case "sha256":
		if len(hex) != 64 {
			return errors.New("sha256 content_hash must be 64 hex characters")
		}
	case "sha1":
		if len(hex) != 40 {
			return errors.New("sha1 content_hash must be 40 hex characters")
		}
	default:
		return fmt.Errorf("unsupported content_hash algorithm %q", algo)
	}
	return nil
}

func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func validOpState(s OpState) bool {
	switch s {
	case OpStateUnknown, OpStateReceiving, OpStateValidating, OpStateCommitting, OpStateCommitted, OpStateRejected:
		return true
	}
	return false
}
