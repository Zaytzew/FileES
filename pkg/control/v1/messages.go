// Package v1 defines the transport-neutral FileES control-plane protocol.
package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"filees/pkg/realmalias"
	publicmanifest "filees/public-shares/manifest"
	publicslug "filees/public-shares/slug"

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
	// TicketLoadRepositoryDump replaces a fresh, single-carrier-commit
	// repository's FSFS with the content of a user-supplied SVN dump. The
	// payload never carries a path or revision: the worker always resolves
	// the carrier as the sole file present on the target's own HEAD
	// (LOAD_REPOSITORY_DUMP_CONCEPT.md §3, §4).
	TicketLoadRepositoryDump  TicketType = "LOAD_REPOSITORY_DUMP"
	TicketGrantAccess         TicketType = "GRANT_ACCESS"
	TicketRevokeAccess        TicketType = "REVOKE_ACCESS"
	TicketListGrantRecipients TicketType = "LIST_GRANT_RECIPIENTS"
	TicketSetRealmVisibility  TicketType = "SET_REALM_DIRECTORY_VISIBILITY"
	TicketListPublicShares    TicketType = "LIST_PUBLIC_SHARES"
	TicketCreatePublicShare   TicketType = "CREATE_PUBLIC_SHARE"
	TicketUpdatePublicShare   TicketType = "UPDATE_PUBLIC_SHARE"
	TicketRevokePublicShare   TicketType = "REVOKE_PUBLIC_SHARE"
	TicketDeletePublicShare   TicketType = "DELETE_PUBLIC_SHARE"
	// payload carries no realm: the worker derives the owner from the
	// authenticated session, and a realm grant - including rw - never
	// satisfies ownership of a repository-wide policy.
	TicketSetRepositoryEditingPolicy TicketType = "SET_REPOSITORY_EDITING_POLICY"
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

type GrantAccessPayload struct {
	RepoID           string `json:"repo_id"`
	RecipientRealmID string `json:"recipient_realm_id"`
	Access           string `json:"access"`
}
type RevokeAccessPayload struct {
	RepoID           string `json:"repo_id"`
	RecipientRealmID string `json:"recipient_realm_id"`
}
type RealmGrantResult struct {
	RepoID           string `json:"repo_id"`
	RecipientRealmID string `json:"recipient_realm_id"`
	Access           string `json:"access,omitempty"`
	State            string `json:"state"`
}
type ListGrantRecipientsPayload struct {
	RepoID string `json:"repo_id,omitempty"`
}
type GrantRecipient struct {
	RealmID string `json:"realm_id"`
	Alias   string `json:"alias"`
	Access  string `json:"access,omitempty"`
	State   string `json:"state,omitempty"`
}
type ListGrantRecipientsResult struct {
	Recipients []GrantRecipient `json:"recipients"`
}
type SetRealmDirectoryVisibilityPayload struct {
	Visibility string `json:"visibility"`
}
type SetRealmDirectoryVisibilityResult struct {
	Visibility string `json:"visibility"`
}

type SetRepositoryEditingPolicyPayload struct {
	RepoID string `json:"repo_id"`
	Policy string `json:"policy"`
}
type SetRepositoryEditingPolicyResult struct {
	RepoID string `json:"repo_id"`
	Policy string `json:"policy"`
}

type PublicShareObject struct {
	PublicID    string `json:"public_id"`
	RepoPath    string `json:"repo_path"`
	DisplayName string `json:"display_name"`
}

// PublicShareDeclaration carries no owner realm: the worker always derives it
// from the authenticated session. PasswordHash is a verifier prepared on the
// client; plaintext passwords must never enter the SVN-backed ticket stream.
type PublicShareDeclaration struct {
	RepoID       string              `json:"repo_id"`
	SourceRoot   string              `json:"source_root"`
	Slug         string              `json:"slug"`
	Recipients   []string            `json:"recipients,omitempty"`
	PasswordHash string              `json:"password_hash,omitempty"`
	DoNotFollow  *int64              `json:"do-not-follow,omitempty"`
	Objects      []PublicShareObject `json:"object_map"`
}

type CreatePublicSharePayload struct{ PublicShareDeclaration }
type UpdatePublicSharePayload struct {
	ChannelID    string `json:"channel_id"`
	KeepPassword bool   `json:"keep_password,omitempty"`
	PublicShareDeclaration
}
type ListPublicSharesPayload struct {
	RepoID string `json:"repo_id"`
}
type PublicShareSummary struct {
	ChannelID         string              `json:"channel_id"`
	RepoID            string              `json:"repo_id"`
	Alias             string              `json:"alias"`
	Slug              string              `json:"slug"`
	State             string              `json:"state"`
	SourceRoot        string              `json:"source_root"`
	Recipients        []string            `json:"recipients,omitempty"`
	PasswordProtected bool                `json:"password_protected,omitempty"`
	DoNotFollow       *int64              `json:"do-not-follow,omitempty"`
	Objects           []PublicShareObject `json:"object_map"`
	UpdatedAt         string              `json:"updated_at"`
}
type ListPublicSharesResult struct {
	Shares []PublicShareSummary `json:"shares"`
}
type RevokePublicSharePayload struct {
	ChannelID string `json:"channel_id"`
}
type DeletePublicSharePayload struct {
	ChannelID string `json:"channel_id"`
}
type PublicShareResult struct {
	ChannelID           string `json:"channel_id"`
	Alias               string `json:"alias"`
	Slug                string `json:"slug"`
	State               string `json:"state"`
	RecipientDeliveries int    `json:"recipient_deliveries,omitempty"`
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
	AdminContact         string `json:"admin_contact"`
}
type RealmRemoveConfirmPayload struct {
	OTP string `json:"otp"`
}
type RealmRemoveConfirmResult struct {
	State            string                `json:"state"`
	Manifest         RealmRecoveryManifest `json:"manifest"`
	AdminContact     string                `json:"admin_contact"`
	ErasureRequested bool                  `json:"erasure_requested"`
	ErasureMaxDays   int                   `json:"erasure_max_days,omitempty"`
}

// LoadRepositoryDumpPayload carries no path and no revision: the worker
// always resolves the carrier itself, from the target's own HEAD, never from
// a client-supplied specific (LOAD_REPOSITORY_DUMP_CONCEPT.md §3).
type LoadRepositoryDumpPayload struct {
	RepoID                   string `json:"repo_id"`
	ApplyCurrentIgnorePolicy bool   `json:"apply_current_ignore_policy,omitempty"`
	KeepLastRevisions        *int   `json:"keep_last_revisions,omitempty"`
}
type LoadRepositoryDumpResult struct {
	RepoID  string `json:"repo_id"`
	OldUUID string `json:"old_uuid"`
	NewUUID string `json:"new_uuid"`
	// SourceRevisionRange is the range actually materialized after filtering
	// and any keep_last_revisions bound - what happened, not what was asked.
	SourceRevisionRange string            `json:"source_revision_range"`
	IgnorePolicyVersion string            `json:"ignore_policy_version,omitempty"`
	ToolVersions        map[string]string `json:"tool_versions"`
}
type RealmRecoveryArchive struct {
	ArchiveID string `json:"archive_id"`
	RepoID    string `json:"repo_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}
type RealmRecoveryManifest struct {
	Schema          string                 `json:"schema"`
	OperationID     string                 `json:"operation_id"`
	RealmID         string                 `json:"realm_id"`
	Archives        []RealmRecoveryArchive `json:"archives"`
	DownloadUntil   time.Time              `json:"download_until"`
	AdminGraceUntil time.Time              `json:"admin_grace_until"`
	CreatedAt       time.Time              `json:"created_at"`
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
	case TicketGrantAccess:
		var p GrantAccessPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("GRANT_ACCESS payload: %w", err)
		}
		if err := validateUUID("GRANT_ACCESS payload.repo_id", p.RepoID); err != nil {
			return err
		}
		if err := validateUUID("GRANT_ACCESS payload.recipient_realm_id", p.RecipientRealmID); err != nil {
			return err
		}
		if p.Access != "r" && p.Access != "rw" {
			return errors.New("GRANT_ACCESS payload.access must be r or rw")
		}
	case TicketRevokeAccess:
		var p RevokeAccessPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("REVOKE_ACCESS payload: %w", err)
		}
		if err := validateUUID("REVOKE_ACCESS payload.repo_id", p.RepoID); err != nil {
			return err
		}
		if err := validateUUID("REVOKE_ACCESS payload.recipient_realm_id", p.RecipientRealmID); err != nil {
			return err
		}
	case TicketListGrantRecipients:
		var p ListGrantRecipientsPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("LIST_GRANT_RECIPIENTS payload: %w", err)
		}
		if p.RepoID != "" {
			if err := validateUUID("LIST_GRANT_RECIPIENTS payload.repo_id", p.RepoID); err != nil {
				return err
			}
		}
	case TicketSetRealmVisibility:
		var p SetRealmDirectoryVisibilityPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("SET_REALM_DIRECTORY_VISIBILITY payload: %w", err)
		}
		if p.Visibility != "hidden" && p.Visibility != "listed" {
			return errors.New("SET_REALM_DIRECTORY_VISIBILITY visibility must be hidden or listed")
		}
	case TicketSetRepositoryEditingPolicy:
		var p SetRepositoryEditingPolicyPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("SET_REPOSITORY_EDITING_POLICY payload: %w", err)
		}
		if _, err := uuid.Parse(p.RepoID); err != nil {
			return errors.New("SET_REPOSITORY_EDITING_POLICY repo_id must be a UUID")
		}
		// Both spellings of the default are accepted on the wire; the worker
		// normalises before storing so the empty form is what gets projected.
		if p.Policy != "" && p.Policy != "free" && p.Policy != "lock_required" {
			return errors.New("SET_REPOSITORY_EDITING_POLICY policy must be free or lock_required")
		}
	case TicketListPublicShares:
		var p ListPublicSharesPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("LIST_PUBLIC_SHARES payload: %w", err)
		}
		if err := validateUUID("LIST_PUBLIC_SHARES payload.repo_id", p.RepoID); err != nil {
			return err
		}
	case TicketCreatePublicShare:
		var p CreatePublicSharePayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("CREATE_PUBLIC_SHARE payload: %w", err)
		}
		if err := validatePublicShareDeclaration(p.PublicShareDeclaration); err != nil {
			return fmt.Errorf("CREATE_PUBLIC_SHARE payload: %w", err)
		}
	case TicketUpdatePublicShare:
		var p UpdatePublicSharePayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("UPDATE_PUBLIC_SHARE payload: %w", err)
		}
		if err := validateUUID("UPDATE_PUBLIC_SHARE payload.channel_id", p.ChannelID); err != nil {
			return err
		}
		if err := validatePublicShareDeclaration(p.PublicShareDeclaration); err != nil {
			return fmt.Errorf("UPDATE_PUBLIC_SHARE payload: %w", err)
		}
		if p.KeepPassword && (p.PasswordHash != "" || len(p.Recipients) > 0) {
			return errors.New("UPDATE_PUBLIC_SHARE keep_password requires an open declaration without a new verifier")
		}
	case TicketRevokePublicShare:
		var p RevokePublicSharePayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("REVOKE_PUBLIC_SHARE payload: %w", err)
		}
		if err := validateUUID("REVOKE_PUBLIC_SHARE payload.channel_id", p.ChannelID); err != nil {
			return err
		}
	case TicketDeletePublicShare:
		var p DeletePublicSharePayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("DELETE_PUBLIC_SHARE payload: %w", err)
		}
		if err := validateUUID("DELETE_PUBLIC_SHARE payload.channel_id", p.ChannelID); err != nil {
			return err
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
	case TicketLoadRepositoryDump:
		var p LoadRepositoryDumpPayload
		if err := decodeStrict(t.Payload, &p); err != nil {
			return fmt.Errorf("LOAD_REPOSITORY_DUMP payload: %w", err)
		}
		if err := validateUUID("LOAD_REPOSITORY_DUMP payload.repo_id", p.RepoID); err != nil {
			return err
		}
		if p.KeepLastRevisions != nil && *p.KeepLastRevisions < 1 {
			return errors.New("LOAD_REPOSITORY_DUMP payload.keep_last_revisions must be at least 1")
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
	if r.Type != TicketStoragePreflight && r.Type != TicketCreateRepository && r.Type != TicketInitialCommit && r.Type != TicketDeleteRepository && r.Type != TicketMobilePairing && r.Type != TicketClaimRealmAlias && r.Type != TicketResolveOwnerLabels && r.Type != TicketClientDeactivate && r.Type != TicketRealmRemoveRequest && r.Type != TicketRealmRemoveConfirm && r.Type != TicketLoadRepositoryDump && r.Type != TicketGrantAccess && r.Type != TicketRevokeAccess && r.Type != TicketListGrantRecipients && r.Type != TicketSetRealmVisibility && r.Type != TicketListPublicShares && r.Type != TicketCreatePublicShare && r.Type != TicketUpdatePublicShare && r.Type != TicketRevokePublicShare && r.Type != TicketDeletePublicShare && r.Type != TicketSetRepositoryEditingPolicy {
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
	case TicketGrantAccess, TicketRevokeAccess:
		var result RealmGrantResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("realm grant result: %w", err)
		}
		if err := validateUUID("realm grant result.repo_id", result.RepoID); err != nil {
			return err
		}
		if err := validateUUID("realm grant result.recipient_realm_id", result.RecipientRealmID); err != nil {
			return err
		}
		if r.Type == TicketGrantAccess && (result.State != "active" || (result.Access != "r" && result.Access != "rw")) {
			return errors.New("GRANT_ACCESS result is invalid")
		}
		if r.Type == TicketRevokeAccess && (result.State != "revoked" || result.Access != "") {
			return errors.New("REVOKE_ACCESS result is invalid")
		}
	case TicketListGrantRecipients:
		var result ListGrantRecipientsResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("LIST_GRANT_RECIPIENTS result: %w", err)
		}
		if len(result.Recipients) > 1024 {
			return errors.New("LIST_GRANT_RECIPIENTS result is too large")
		}
		seen := map[string]bool{}
		for _, recipient := range result.Recipients {
			if err := validateUUID("LIST_GRANT_RECIPIENTS result.realm_id", recipient.RealmID); err != nil {
				return err
			}
			if seen[recipient.RealmID] {
				return errors.New("LIST_GRANT_RECIPIENTS result contains duplicate realm")
			}
			seen[recipient.RealmID] = true
			if _, err := realmalias.Normalize(recipient.Alias); err != nil {
				return fmt.Errorf("LIST_GRANT_RECIPIENTS alias: %w", err)
			}
			if recipient.State == "" {
				if recipient.Access != "" {
					return errors.New("LIST_GRANT_RECIPIENTS ungranted recipient has access")
				}
				continue
			}
			if recipient.State != "active" && recipient.State != "revoked" {
				return errors.New("LIST_GRANT_RECIPIENTS result has invalid grant state")
			}
			if recipient.State == "active" && recipient.Access != "r" && recipient.Access != "rw" {
				return errors.New("LIST_GRANT_RECIPIENTS active grant has invalid access")
			}
			if recipient.State == "revoked" && recipient.Access != "" {
				return errors.New("LIST_GRANT_RECIPIENTS revoked grant has access")
			}
		}
	case TicketSetRealmVisibility:
		var result SetRealmDirectoryVisibilityResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("SET_REALM_DIRECTORY_VISIBILITY result: %w", err)
		}
		if result.Visibility != "hidden" && result.Visibility != "listed" {
			return errors.New("SET_REALM_DIRECTORY_VISIBILITY result is invalid")
		}
	case TicketSetRepositoryEditingPolicy:
		var result SetRepositoryEditingPolicyResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("SET_REPOSITORY_EDITING_POLICY result: %w", err)
		}
		if _, err := uuid.Parse(result.RepoID); err != nil {
			return errors.New("SET_REPOSITORY_EDITING_POLICY result repo_id must be a UUID")
		}
		// The result reports the stored value, which is already normalised:
		// the default is the empty string and never the "free" alias.
		if result.Policy != "" && result.Policy != "lock_required" {
			return errors.New("SET_REPOSITORY_EDITING_POLICY result policy is invalid")
		}
	case TicketListPublicShares:
		var result ListPublicSharesResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("LIST_PUBLIC_SHARES result: %w", err)
		}
		if len(result.Shares) > 1024 {
			return errors.New("LIST_PUBLIC_SHARES result is too large")
		}
		seen := map[string]bool{}
		for _, share := range result.Shares {
			if err := validateUUID("public share summary.channel_id", share.ChannelID); err != nil {
				return err
			}
			if seen[share.ChannelID] {
				return errors.New("LIST_PUBLIC_SHARES result contains duplicate channel")
			}
			seen[share.ChannelID] = true
			if _, err := realmalias.Normalize(share.Alias); err != nil {
				return errors.New("public share summary alias is invalid")
			}
			if share.State != "active" && share.State != "revoked" {
				return errors.New("public share summary state is invalid")
			}
			declaration := PublicShareDeclaration{RepoID: share.RepoID, SourceRoot: share.SourceRoot, Slug: share.Slug, Recipients: share.Recipients, DoNotFollow: share.DoNotFollow, Objects: share.Objects}
			if err := validatePublicShareDeclaration(declaration); err != nil {
				return fmt.Errorf("public share summary: %w", err)
			}
			if _, err := time.Parse(time.RFC3339Nano, share.UpdatedAt); err != nil {
				return errors.New("public share summary updated_at is invalid")
			}
		}
	case TicketCreatePublicShare, TicketUpdatePublicShare, TicketRevokePublicShare, TicketDeletePublicShare:
		var result PublicShareResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("public share result: %w", err)
		}
		if err := validateUUID("public share result.channel_id", result.ChannelID); err != nil {
			return err
		}
		if _, err := realmalias.Normalize(result.Alias); err != nil {
			return errors.New("public share result alias is invalid")
		}
		if _, err := publicslug.Normalize(result.Slug); err != nil {
			return errors.New("public share result slug is invalid")
		}
		if result.RecipientDeliveries < 0 {
			return errors.New("public share result recipient_deliveries cannot be negative")
		}
		wantState := "active"
		if r.Type == TicketRevokePublicShare {
			wantState = "revoked"
		}
		if r.Type == TicketDeletePublicShare {
			wantState = "deleted"
		}
		if result.State != wantState {
			return errors.New("public share result state is invalid")
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
		if _, err := validatePlainEmail(result.AdminContact); err != nil {
			return errors.New("REALM_REMOVE_REQUEST admin contact is invalid")
		}
	case TicketRealmRemoveConfirm:
		var result RealmRemoveConfirmResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("REALM_REMOVE_CONFIRM result: %w", err)
		}
		if result.State != "completed" {
			return errors.New("REALM_REMOVE_CONFIRM result state is invalid")
		}
		if err := validateRealmRecoveryManifest(result.Manifest); err != nil {
			return fmt.Errorf("REALM_REMOVE_CONFIRM recovery manifest: %w", err)
		}
		if result.Manifest.OperationID != r.OperationID {
			return errors.New("REALM_REMOVE_CONFIRM manifest belongs to another operation")
		}
		if _, err := validatePlainEmail(result.AdminContact); err != nil {
			return errors.New("REALM_REMOVE_CONFIRM admin contact is invalid")
		}
	case TicketLoadRepositoryDump:
		var result LoadRepositoryDumpResult
		if err := decodeStrict(r.Result, &result); err != nil {
			return fmt.Errorf("LOAD_REPOSITORY_DUMP result: %w", err)
		}
		if err := validateUUID("LOAD_REPOSITORY_DUMP result.repo_id", result.RepoID); err != nil {
			return err
		}
		if strings.TrimSpace(result.OldUUID) == "" || strings.TrimSpace(result.NewUUID) == "" {
			return errors.New("LOAD_REPOSITORY_DUMP result requires old_uuid and new_uuid")
		}
		if result.OldUUID == result.NewUUID {
			return errors.New("LOAD_REPOSITORY_DUMP result must report a fresh new_uuid")
		}
		if strings.TrimSpace(result.SourceRevisionRange) == "" {
			return errors.New("LOAD_REPOSITORY_DUMP result requires source_revision_range")
		}
		if len(result.ToolVersions) == 0 {
			return errors.New("LOAD_REPOSITORY_DUMP result requires tool_versions")
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

func validatePublicShareDeclaration(p PublicShareDeclaration) error {
	if err := validateUUID("repo_id", p.RepoID); err != nil {
		return err
	}
	if parsed, _ := uuid.Parse(p.RepoID); parsed.String() != p.RepoID {
		return errors.New("repo_id must be a canonical UUID")
	}
	normalizedSlug, err := publicslug.Normalize(p.Slug)
	if err != nil || normalizedSlug != p.Slug {
		return errors.New("slug must be normalized")
	}
	if err := validatePublicSharePath("source_root", p.SourceRoot); err != nil {
		return err
	}
	if len(p.Recipients) > 256 {
		return errors.New("recipients exceeds 256 addresses")
	}
	if len(p.Recipients) > 0 && p.PasswordHash != "" {
		return errors.New("closed share cannot carry a shared password")
	}
	if p.PasswordHash != "" {
		if len(p.PasswordHash) > 512 || publicmanifest.ValidatePasswordVerifier(p.PasswordHash) != nil {
			return errors.New("password_hash is not a bounded Argon2id verifier")
		}
	}
	seenRecipients := map[string]bool{}
	for _, raw := range p.Recipients {
		email, err := validatePlainEmail(raw)
		if err != nil {
			return errors.New("recipient address is invalid")
		}
		key := strings.ToLower(email)
		if seenRecipients[key] {
			return errors.New("recipient list contains duplicate address")
		}
		seenRecipients[key] = true
	}
	if p.DoNotFollow != nil && *p.DoNotFollow < 1 {
		return errors.New("do-not-follow must be at least 1")
	}
	if len(p.Objects) == 0 || len(p.Objects) > 4096 {
		return errors.New("object_map must contain 1 to 4096 objects")
	}
	seenObjects := map[string]bool{}
	rootPrefix := p.SourceRoot + "/"
	for i, object := range p.Objects {
		if len(object.PublicID) < 16 || len(object.PublicID) > 64 {
			return fmt.Errorf("object_map[%d].public_id has invalid length", i)
		}
		for _, ch := range object.PublicID {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
				return fmt.Errorf("object_map[%d].public_id is invalid", i)
			}
		}
		if seenObjects[object.PublicID] {
			return errors.New("object_map contains duplicate public_id")
		}
		seenObjects[object.PublicID] = true
		if err := validatePublicSharePath(fmt.Sprintf("object_map[%d].repo_path", i), object.RepoPath); err != nil {
			return err
		}
		if !strings.HasPrefix(object.RepoPath, rootPrefix) {
			return fmt.Errorf("object_map[%d].repo_path is outside source_root", i)
		}
		if strings.TrimSpace(object.DisplayName) == "" || len(object.DisplayName) > 512 || strings.ContainsAny(object.DisplayName, "\x00\r\n") {
			return fmt.Errorf("object_map[%d].display_name is invalid", i)
		}
	}
	return nil
}

func validatePublicSharePath(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\x00") || strings.Contains(value, "//") {
		return fmt.Errorf("%s is not a canonical repository-relative path", field)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%s contains a relative segment", field)
		}
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

func validateRealmRecoveryManifest(manifest RealmRecoveryManifest) error {
	if manifest.Schema != "filees.realm-recovery-manifest/v1" {
		return errors.New("schema is invalid")
	}
	if err := validateUUID("operation_id", manifest.OperationID); err != nil {
		return err
	}
	if err := validateUUID("realm_id", manifest.RealmID); err != nil {
		return err
	}
	if manifest.CreatedAt.IsZero() || manifest.DownloadUntil.Before(manifest.CreatedAt) || manifest.AdminGraceUntil.Before(manifest.DownloadUntil) {
		return errors.New("retention is invalid")
	}
	seen := map[string]bool{}
	for _, archive := range manifest.Archives {
		if validateUUID("archive_id", archive.ArchiveID) != nil || validateUUID("repo_id", archive.RepoID) != nil || seen[archive.ArchiveID] || archive.Size < 0 || archive.Size > 1<<50 || len(archive.SHA256) != sha256.Size*2 {
			return errors.New("archive is invalid")
		}
		if _, err := hex.DecodeString(archive.SHA256); err != nil {
			return errors.New("archive digest is invalid")
		}
		seen[archive.ArchiveID] = true
	}
	return nil
}
