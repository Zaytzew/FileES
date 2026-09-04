// Package clientview validates the server-owned projection consumed by a
// FileES client daemon. The projection is knowledge, never local authority.
package clientview

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"filees/internal/durable"
	"filees/pkg/realmalias"

	"github.com/google/uuid"
)

const Schema = "filees.client-view/v2"

const MaxServerDisplayNameRunes = 80

type View struct {
	Schema               string               `json:"schema"`
	ServerDisplayName    string               `json:"server_display_name"`
	ClientID             string               `json:"client_id"`
	RealmID              string               `json:"realm_id"`
	RealmAlias           string               `json:"realm_alias,omitempty"`
	Generation           int64                `json:"generation"`
	GeneratedAt          time.Time            `json:"generated_at"`
	MinimumClientVersion string               `json:"minimum_client_version,omitempty"`
	ClientRole           string               `json:"client_role"`
	Capabilities         *Capabilities        `json:"capabilities,omitempty"`
	Repositories         []Repository         `json:"repositories"`
	LockReleaseRequests  []LockReleaseRequest `json:"lock_release_requests,omitempty"`
	ActiveOperations     []json.RawMessage    `json:"active_operations"`
}

// ValidateServerDisplayName guards the human-facing server label carried by
// every projection. The immutable server_id remains transport identity; this
// value is presentation and may be changed by the operator.
func ValidateServerDisplayName(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("must be non-empty and have no surrounding whitespace")
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxServerDisplayNameRunes {
		return fmt.Errorf("must be valid UTF-8 and at most %d characters", MaxServerDisplayNameRunes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

type Capabilities struct {
	CanCreateRepositories bool `json:"can_create_repositories"`
}

// Editing policies. EditingFree is deliberately the empty string: Decode
// rejects unknown fields, so a projection carrying editing_policy is
// unreadable to any binary that predates it — the whole view fails, not one
// repository. Representing the default as absence keeps the field out of
// every projection until an owner actually opts a repository in, which makes
// the rollout inert instead of breaking. Never serialise EditingFree.
const (
	EditingFree         = ""
	EditingLockRequired = "lock_required"
)

// ValidEditingPolicy reports whether policy is one this build understands.
// "free" is accepted on input as an explicit spelling of the default, but
// callers must normalise it to EditingFree before storing or projecting.
func ValidEditingPolicy(policy string) bool {
	return policy == EditingFree || policy == "free" || policy == EditingLockRequired
}

// NormalizeEditingPolicy collapses the explicit "free" spelling onto the
// empty default so it never reaches the wire.
func NormalizeEditingPolicy(policy string) string {
	if policy == "free" {
		return EditingFree
	}
	return policy
}

type Repository struct {
	RepoID           string `json:"repo_id"`
	DisplayName      string `json:"display_name"`
	URL              string `json:"url"`
	Access           string `json:"access"`
	State            string `json:"state"`
	OwnerRealmID     string `json:"owner_realm_id,omitempty"`
	AttachmentPolicy string `json:"attachment_policy,omitempty"`
	MetadataDigest   string `json:"metadata_digest,omitempty"`
	// EditingPolicy is repository-wide and always sourced from the canonical
	// repository record, never carried over from a previous projection the
	// way AttachmentPolicy is — that one is a per-client grant, this one is
	// a property of the repository itself.
	EditingPolicy string `json:"editing_policy,omitempty"`
	// Purpose is empty for an ordinary project repository. Upload Channel
	// stamps upload_shelf on the delivery repo and upload_trash on the
	// realm-wide reject quarantine. Absence keeps old projections readable.
	Purpose string `json:"purpose,omitempty"`
}

type LockReleaseRequest struct {
	RequestID              string    `json:"request_id"`
	RepoID                 string    `json:"repo_id"`
	Path                   string    `json:"path"`
	ObservedLockID         string    `json:"observed_lock_id"`
	Role                   string    `json:"role"` // requester or holder in this projection
	CounterpartyRealmAlias string    `json:"counterparty_realm_alias,omitempty"`
	State                  string    `json:"state"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
	ExpiresAt              time.Time `json:"expires_at"`
}

const (
	PurposeNone        = ""
	PurposeUploadShelf = "upload_shelf"
	PurposeUploadTrash = "upload_trash"
)

func ValidPurpose(purpose string) bool {
	return purpose == PurposeNone || purpose == PurposeUploadShelf || purpose == PurposeUploadTrash
}

// RequiresLock reports whether editing this repository goes through the edit
// passport machinery rather than plain merge-on-commit.
func (r Repository) RequiresLock() bool { return r.EditingPolicy == EditingLockRequired }

func (v View) CanCreateRepositories() bool {
	if v.ClientRole == "ro" {
		return false
	}
	if v.Capabilities == nil {
		return true // compatibility with pre-capability v1 projections
	}
	return v.Capabilities.CanCreateRepositories
}

func Load(path string) (View, error) {
	f, err := os.Open(path)
	if err != nil {
		return View{}, err
	}
	defer f.Close()
	return Decode(f)
}

func Decode(r io.Reader) (View, error) {
	decoder := json.NewDecoder(io.LimitReader(r, 1<<20))
	decoder.DisallowUnknownFields()
	var view View
	if err := decoder.Decode(&view); err != nil {
		return View{}, fmt.Errorf("decode client view: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return View{}, errors.New("client view contains trailing data")
	}
	if err := view.Validate(); err != nil {
		return View{}, err
	}
	return view, nil
}

func (v View) Validate() error {
	if v.Schema != Schema {
		return fmt.Errorf("unsupported client view schema %q", v.Schema)
	}
	if err := ValidateServerDisplayName(v.ServerDisplayName); err != nil {
		return fmt.Errorf("client view server_display_name: %w", err)
	}
	if _, err := uuid.Parse(v.ClientID); err != nil {
		return errors.New("client view client_id must be UUID")
	}
	if _, err := uuid.Parse(v.RealmID); err != nil {
		return errors.New("client view realm_id must be UUID")
	}
	if v.RealmAlias != "" {
		canonical, err := realmalias.Normalize(v.RealmAlias)
		if err != nil || canonical != v.RealmAlias {
			return errors.New("client view realm_alias is invalid")
		}
	}
	if v.Generation < 1 || v.GeneratedAt.IsZero() {
		return errors.New("client view generation and generated_at are required")
	}
	if v.ClientRole != "normal" && v.ClientRole != "ro" {
		return errors.New("client view client_role must be normal or ro")
	}
	if v.ClientRole == "ro" && v.Capabilities != nil && v.Capabilities.CanCreateRepositories {
		return errors.New("client view read-only role cannot create repositories")
	}
	seen := make(map[string]struct{}, len(v.Repositories))
	for i, repo := range v.Repositories {
		if _, err := uuid.Parse(repo.RepoID); err != nil {
			return fmt.Errorf("repositories[%d].repo_id must be UUID", i)
		}
		if _, ok := seen[repo.RepoID]; ok {
			return fmt.Errorf("repositories[%d].repo_id is duplicated", i)
		}
		seen[repo.RepoID] = struct{}{}
		if strings.TrimSpace(repo.DisplayName) == "" || strings.ContainsAny(repo.DisplayName, "\x00\r\n") {
			return fmt.Errorf("repositories[%d].display_name is invalid", i)
		}
		parsed, err := url.Parse(repo.URL)
		if err != nil || parsed.Scheme != "svn+ssh" || parsed.Hostname() == "" || parsed.User == nil || (parsed.User.Username() != "_filees-client" && parsed.User.Username() != "_filees-data") || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("repositories[%d].url must use restricted svn+ssh transport", i)
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return fmt.Errorf("repositories[%d].url contains a password", i)
		}
		if repo.Access != "r" && repo.Access != "rw" {
			return fmt.Errorf("repositories[%d].access must be r or rw", i)
		}
		if v.ClientRole == "ro" && repo.Access != "r" {
			return fmt.Errorf("repositories[%d].access exceeds global read-only role", i)
		}
		if repo.State != "initializing" && repo.State != "active" && repo.State != "disabled" && repo.State != "revoked" {
			return fmt.Errorf("repositories[%d].state is invalid", i)
		}
		if repo.OwnerRealmID != "" {
			if _, err := uuid.Parse(repo.OwnerRealmID); err != nil {
				return fmt.Errorf("repositories[%d].owner_realm_id must be UUID", i)
			}
		}
		if repo.AttachmentPolicy != "" && repo.AttachmentPolicy != "optional" && repo.AttachmentPolicy != "required" {
			return fmt.Errorf("repositories[%d].attachment_policy is invalid", i)
		}
		// Only the canonical spellings reach a projection: "free" is an input
		// alias that must have been normalised to absence before storing, so
		// seeing it here means an unnormalised value escaped a writer.
		if repo.EditingPolicy != EditingFree && repo.EditingPolicy != EditingLockRequired {
			return fmt.Errorf("repositories[%d].editing_policy is invalid", i)
		}
		if !ValidPurpose(repo.Purpose) {
			return fmt.Errorf("repositories[%d].purpose is invalid", i)
		}
	}
	requestIDs := make(map[string]struct{}, len(v.LockReleaseRequests))
	for i, request := range v.LockReleaseRequests {
		if _, err := uuid.Parse(request.RequestID); err != nil {
			return fmt.Errorf("lock_release_requests[%d].request_id must be UUID", i)
		}
		if _, duplicate := requestIDs[request.RequestID]; duplicate {
			return fmt.Errorf("lock_release_requests[%d].request_id is duplicated", i)
		}
		requestIDs[request.RequestID] = struct{}{}
		if _, exists := seen[request.RepoID]; !exists {
			return fmt.Errorf("lock_release_requests[%d].repo_id is not projected", i)
		}
		if request.Path == "" || len(request.Path) > 4096 || request.Path != strings.TrimSpace(request.Path) || strings.HasPrefix(request.Path, "/") || strings.ContainsAny(request.Path, "\\\x00\r\n") || path.Clean(request.Path) != request.Path || request.Path == "." || strings.HasPrefix(request.Path, "../") {
			return fmt.Errorf("lock_release_requests[%d].path is invalid", i)
		}
		if request.ObservedLockID == "" || len(request.ObservedLockID) > 2048 || request.ObservedLockID != strings.TrimSpace(request.ObservedLockID) || strings.ContainsAny(request.ObservedLockID, "\x00\r\n") {
			return fmt.Errorf("lock_release_requests[%d].observed_lock_id is invalid", i)
		}
		if request.Role != "requester" && request.Role != "holder" {
			return fmt.Errorf("lock_release_requests[%d].role is invalid", i)
		}
		if request.CounterpartyRealmAlias != "" {
			canonical, err := realmalias.Normalize(request.CounterpartyRealmAlias)
			if err != nil || canonical != request.CounterpartyRealmAlias {
				return fmt.Errorf("lock_release_requests[%d].counterparty_realm_alias is invalid", i)
			}
		}
		switch request.State {
		case "pending", "dismissed", "accepted", "lock_gone", "expired", "stale":
		default:
			return fmt.Errorf("lock_release_requests[%d].state is invalid", i)
		}
		if request.CreatedAt.IsZero() || request.UpdatedAt.Before(request.CreatedAt) || !request.ExpiresAt.After(request.CreatedAt) {
			return fmt.Errorf("lock_release_requests[%d] timestamps are invalid", i)
		}
	}
	return nil
}

// StoreIfNewer atomically publishes a validated projection. Equal generation
// is idempotent only for byte-equivalent semantic content; older data is
// rejected so recovery cannot roll effective permissions back.
func StoreIfNewer(path string, next View) (bool, error) {
	if err := next.Validate(); err != nil {
		return false, err
	}
	if current, err := Load(path); err == nil {
		if next.ClientID != current.ClientID || next.RealmID != current.RealmID {
			return false, errors.New("client view identity changed")
		}
		if next.Generation < current.Generation {
			return false, errors.New("client view generation rollback")
		}
		if next.Generation == current.Generation {
			a, _ := json.Marshal(current)
			b, _ := json.Marshal(next)
			if string(a) != string(b) {
				return false, errors.New("client view generation conflicts with cached content")
			}
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return false, err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".view-*.tmp")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, err
	}
	if err := durable.SyncDirectory(filepath.Dir(path)); err != nil {
		return false, err
	}
	return true, nil
}
