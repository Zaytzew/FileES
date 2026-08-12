// Package channel owns the durable Public Shares channel lifecycle.
//
// Record is authoritative and may contain repository paths. Projection is the
// deliberately weaker object handed to the public service: its type cannot
// represent a repository path, so an accidental JSON projection cannot leak
// the authoritative object map.
package channel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/pkg/realmbranding"
	"filees/public-shares/manifest"
	"filees/public-shares/slug"
	"github.com/google/uuid"
)

const (
	RecordSchema     = "filees.public-share-channel/v1"
	ProjectionSchema = "filees.public-share-projection/v1"
	SlugSchema       = "filees.public-share-slug/v1"
	StateActive      = "active"
	StateRevoked     = "revoked"
	StateDeleted     = "deleted"
)

var (
	ErrNotFound       = errors.New("public share channel not found")
	ErrForbidden      = errors.New("public share channel is not owned by requester")
	ErrSlugTaken      = errors.New("public share slug cannot be assigned")
	ErrInactive       = errors.New("public share channel is not active")
	ErrRecordConflict = errors.New("public share channel conflicts with prior operation")
	ErrPolicy         = errors.New("public share request exceeds host policy")
)

// RecipientCredential is canonical ACL state. TokenHash is a SHA-256 digest
// of a high-entropy token, never the token itself. Epoch makes a removed and
// later re-added mailbox receive a different token.
type RecipientCredential struct {
	Email     string `json:"email"`
	TokenHash string `json:"token_sha256"`
	Epoch     string `json:"epoch"`
}

type Record struct {
	Schema     string                `json:"schema"`
	ChannelID  string                `json:"channel_id"`
	OwnerRealm string                `json:"owner_realm"`
	RepoID     string                `json:"repo_id"`
	Alias      string                `json:"alias"`
	Slug       string                `json:"slug"`
	State      string                `json:"state"`
	Manifest   *manifest.Share       `json:"manifest,omitempty"`
	Recipients []RecipientCredential `json:"recipient_credentials,omitempty"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
	RevokedAt  *time.Time            `json:"revoked_at,omitempty"`
	DeletedAt  *time.Time            `json:"deleted_at,omitempty"`
}

// PublicObject deliberately has no RepoPath field.
type PublicObject struct {
	PublicID    string `json:"public_id"`
	DisplayName string `json:"display_name"`
	Size        *int64 `json:"size,omitempty"`
}

type PublicRecipient struct {
	Email     string `json:"email"`
	TokenHash string `json:"token_sha256"`
}

// Projection is all policy the public service may know. OwnerRealm, RepoID,
// SourceRoot and RepoPath are absent by construction.
type Projection struct {
	Schema       string                 `json:"schema"`
	ChannelID    string                 `json:"channel_id"`
	Alias        string                 `json:"alias"`
	Slug         string                 `json:"slug"`
	State        string                 `json:"state"`
	PasswordHash string                 `json:"password_hash,omitempty"`
	Recipients   []PublicRecipient      `json:"recipients,omitempty"`
	DoNotFollow  *int64                 `json:"do-not-follow,omitempty"`
	Objects      []PublicObject         `json:"objects"`
	Branding     realmbranding.Branding `json:"branding"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

type Delivery struct {
	Email string
	Token string
}

type RepositoryAuthority interface {
	OwnsActiveRepository(realmID, repoID string) error
	ActiveRealmAlias(realmID string) (string, error)
	ActiveRealmBranding(realmID string) (realmbranding.Branding, error)
}

type Store struct {
	Root                string
	Authority           RepositoryAuthority
	TokenKey            []byte
	MaxChannelsPerRealm int
	PasswordRequired    bool
	Now                 func() time.Time
	mu                  sync.Mutex
}

// Create uses operationID as the stable channel identifier. This makes a
// retried control-plane operation idempotent without accepting a client-chosen
// channel ID in the payload.
func (s *Store) Create(operationID, requesterRealm string, declaration manifest.Share) (Record, []Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return Record{}, nil, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return Record{}, nil, errors.New("public share operation_id must be UUID")
	}
	if declaration.OwnerRealm != requesterRealm {
		return Record{}, nil, ErrForbidden
	}
	if err := declaration.Validate(); err != nil {
		return Record{}, nil, err
	}
	if s.PasswordRequired && len(declaration.Recipients) == 0 && declaration.Password == "" {
		return Record{}, nil, ErrPolicy
	}
	if err := s.Authority.OwnsActiveRepository(requesterRealm, declaration.RepoID); err != nil {
		return Record{}, nil, ErrForbidden
	}
	alias, err := s.Authority.ActiveRealmAlias(requesterRealm)
	if err != nil {
		return Record{}, nil, ErrForbidden
	}
	alias = strings.TrimSpace(alias)
	if _, err := slug.Path(alias, declaration.Slug); err != nil {
		return Record{}, nil, err
	}
	path := s.recordPath(operationID)
	if old, err := s.loadPath(path); err == nil {
		if old.Manifest != nil && old.State == StateActive && sameDeclaration(*old.Manifest, declaration) {
			return old, s.deliveriesForEpoch(old, operationID), nil
		}
		return Record{}, nil, ErrRecordConflict
	} else if !errors.Is(err, ErrNotFound) {
		return Record{}, nil, err
	}
	if s.MaxChannelsPerRealm > 0 {
		count, err := s.channelCount(requesterRealm)
		if err != nil {
			return Record{}, nil, err
		}
		if count >= s.MaxChannelsPerRealm {
			return Record{}, nil, ErrPolicy
		}
	}
	reservation := slugReservation{Schema: SlugSchema, OwnerRealm: requesterRealm, Alias: alias, Slug: declaration.Slug, ChannelID: operationID, CreatedAt: s.now()}
	if err := s.ensureReservation(reservation); err != nil {
		return Record{}, nil, err
	}
	recipients, deliveries, err := s.issueRecipients(operationID, operationID, declaration.Recipients, nil)
	if err != nil {
		return Record{}, nil, err
	}
	now := s.now()
	record := Record{Schema: RecordSchema, ChannelID: operationID, OwnerRealm: requesterRealm, RepoID: declaration.RepoID, Alias: alias, Slug: declaration.Slug, State: StateActive, Manifest: &declaration, Recipients: recipients, CreatedAt: now, UpdatedAt: now}
	if err := atomicJSON(path, 0600, record); err != nil {
		return Record{}, nil, err
	}
	return record, deliveries, nil
}

// Update changes an active declaration. Its repository, owner, alias and slug
// stay fixed; a different public address is a new channel and therefore a new
// tombstone. Existing recipients retain their tokens, while newly added (or
// re-added) addresses receive a fresh operation-bound epoch.
func (s *Store) Update(operationID, requesterRealm, channelID string, declaration manifest.Share) (Record, []Delivery, error) {
	return s.update(operationID, requesterRealm, channelID, declaration, false)
}

// UpdatePreservingPassword keeps the current verifier when an authenticated
// desktop owner edits a password-protected open channel without entering the
// plaintext password again. The verifier never needs to cross the control or
// IPC boundary. It cannot be preserved while switching to recipient tokens.
func (s *Store) UpdatePreservingPassword(operationID, requesterRealm, channelID string, declaration manifest.Share) (Record, []Delivery, error) {
	return s.update(operationID, requesterRealm, channelID, declaration, true)
}

func (s *Store) update(operationID, requesterRealm, channelID string, declaration manifest.Share, preservePassword bool) (Record, []Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return Record{}, nil, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return Record{}, nil, errors.New("public share operation_id must be UUID")
	}
	record, err := s.load(channelID)
	if err != nil {
		return Record{}, nil, err
	}
	if record.Manifest == nil || record.Manifest.OwnerRealm != requesterRealm {
		return Record{}, nil, ErrForbidden
	}
	if record.State != StateActive {
		return Record{}, nil, ErrInactive
	}
	if declaration.OwnerRealm != requesterRealm || declaration.RepoID != record.Manifest.RepoID || declaration.Slug != record.Manifest.Slug {
		return Record{}, nil, ErrRecordConflict
	}
	if preservePassword {
		if len(declaration.Recipients) > 0 || declaration.Password != "" {
			return Record{}, nil, ErrRecordConflict
		}
		declaration.Password = record.Manifest.Password
	}
	if err := declaration.Validate(); err != nil {
		return Record{}, nil, err
	}
	if s.PasswordRequired && len(declaration.Recipients) == 0 && declaration.Password == "" {
		return Record{}, nil, ErrPolicy
	}
	if err := s.Authority.OwnsActiveRepository(requesterRealm, declaration.RepoID); err != nil {
		return Record{}, nil, ErrForbidden
	}
	if sameDeclaration(*record.Manifest, declaration) {
		return record, s.deliveriesForEpoch(record, operationID), nil
	}
	recipients, deliveries, err := s.issueRecipients(channelID, operationID, declaration.Recipients, record.Recipients)
	if err != nil {
		return Record{}, nil, err
	}
	record.Manifest = &declaration
	record.Recipients = recipients
	record.UpdatedAt = s.now()
	if err := atomicJSON(s.recordPath(channelID), 0600, record); err != nil {
		return Record{}, nil, err
	}
	return record, deliveries, nil
}

// ListOwned returns the non-deleted channels for one repository after checking
// the authenticated realm still owns that active repository. Records are
// newest-first and remain authoritative objects; callers must project only the
// fields suitable for their own trust boundary.
func (s *Store) ListOwned(requesterRealm, repoID string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(requesterRealm); err != nil {
		return nil, ErrForbidden
	}
	if _, err := uuid.Parse(repoID); err != nil {
		return nil, ErrNotFound
	}
	if err := s.Authority.OwnsActiveRepository(requesterRealm, repoID); err != nil {
		return nil, ErrForbidden
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "channels"))
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.loadPath(filepath.Join(s.Root, "channels", entry.Name()))
		if err != nil {
			return nil, err
		}
		if record.OwnerRealm == requesterRealm && record.RepoID == repoID && record.State != StateDeleted {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ChannelID < result[j].ChannelID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (s *Store) Revoke(requesterRealm, channelID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.load(channelID)
	if err != nil {
		return Record{}, err
	}
	if record.Manifest == nil || record.Manifest.OwnerRealm != requesterRealm {
		return Record{}, ErrForbidden
	}
	if record.State == StateDeleted {
		return Record{}, ErrNotFound
	}
	if record.State == StateRevoked {
		if err := s.removeOutbox(map[string]bool{channelID: true}); err != nil {
			return Record{}, err
		}
		return record, nil
	}
	now := s.now()
	record.State, record.UpdatedAt, record.RevokedAt = StateRevoked, now, &now
	if err := atomicJSON(s.recordPath(channelID), 0600, record); err != nil {
		return Record{}, err
	}
	if err := s.removeOutbox(map[string]bool{channelID: true}); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Delete removes policy, repository paths and recipient addresses while
// retaining only the non-reusable address tombstone and minimal ownership
// metadata in the channel record.
func (s *Store) Delete(requesterRealm, channelID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.load(channelID)
	if err != nil {
		return Record{}, err
	}
	if record.Manifest == nil {
		if record.State == StateDeleted {
			if err := s.removeOutbox(map[string]bool{channelID: true}); err != nil {
				return Record{}, err
			}
			return record, nil
		}
		return Record{}, ErrForbidden
	}
	if record.Manifest.OwnerRealm != requesterRealm {
		return Record{}, ErrForbidden
	}
	now := s.now()
	record.State, record.UpdatedAt, record.DeletedAt = StateDeleted, now, &now
	record.Manifest, record.Recipients = nil, nil
	if err := atomicJSON(s.recordPath(channelID), 0600, record); err != nil {
		return Record{}, err
	}
	if err := s.removeOutbox(map[string]bool{channelID: true}); err != nil {
		return Record{}, err
	}
	return record, nil
}

// DeleteRealm removes recipient addresses and authoritative paths from every
// channel owned by a removed realm. Slug/address tombstones remain so old links
// can never be rebound. Pending token mail is removed with the channel data.
func (s *Store) DeleteRealm(ownerRealm string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !filepath.IsAbs(s.Root) {
		return 0, errors.New("public share store root must be absolute")
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "channels"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	deleted, channelIDs := 0, map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.Root, "channels", entry.Name())
		record, err := s.loadPath(path)
		if err != nil {
			return deleted, err
		}
		if record.OwnerRealm != ownerRealm {
			continue
		}
		channelIDs[record.ChannelID] = true
		if record.State == StateDeleted {
			continue
		}
		now := s.now()
		record.State, record.UpdatedAt, record.DeletedAt = StateDeleted, now, &now
		record.Manifest, record.Recipients = nil, nil
		if err := atomicJSON(path, 0600, record); err != nil {
			return deleted, err
		}
		deleted++
	}
	if err := s.removeOutbox(channelIDs); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *Store) removeOutbox(channelIDs map[string]bool) error {
	mailEntries, err := os.ReadDir(filepath.Join(s.Root, "outbox"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range mailEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.Root, "outbox", entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var envelope struct {
			ChannelID string `json:"channel_id"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return errors.New("public share outbox record is invalid")
		}
		if channelIDs[envelope.ChannelID] {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Load(channelID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(channelID)
}

func (s *Store) Projection(channelID string) (Projection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.load(channelID)
	if err != nil {
		return Projection{}, err
	}
	branding, err := s.Authority.ActiveRealmBranding(record.OwnerRealm)
	if err != nil {
		return Projection{}, ErrNotFound
	}
	return project(record, branding)
}

// ResolveAddress returns only an active channel. Missing, revoked, deleted and
// malformed addresses deliberately collapse to ErrNotFound.
func (s *Store) ResolveAddress(alias, channelSlug string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := slug.Path(alias, channelSlug); err != nil {
		return Record{}, ErrNotFound
	}
	raw, err := os.ReadFile(s.addressPath(alias, channelSlug))
	if err != nil {
		return Record{}, ErrNotFound
	}
	var reservation slugReservation
	if json.Unmarshal(raw, &reservation) != nil || reservation.Schema != SlugSchema || reservation.Alias != alias || reservation.Slug != channelSlug {
		return Record{}, ErrNotFound
	}
	record, err := s.load(reservation.ChannelID)
	if err != nil || record.State != StateActive || record.Manifest == nil || record.Alias != alias || record.Slug != channelSlug || record.Manifest.Slug != channelSlug {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func project(record Record, branding realmbranding.Branding) (Projection, error) {
	if record.State != StateActive || record.Manifest == nil {
		return Projection{}, ErrNotFound
	}
	p := Projection{Schema: ProjectionSchema, ChannelID: record.ChannelID, Alias: record.Alias, Slug: record.Slug, State: record.State, PasswordHash: record.Manifest.Password, DoNotFollow: record.Manifest.DoNotFollow, Branding: branding, UpdatedAt: record.UpdatedAt}
	for _, recipient := range record.Recipients {
		p.Recipients = append(p.Recipients, PublicRecipient{Email: recipient.Email, TokenHash: recipient.TokenHash})
	}
	for _, object := range record.Manifest.Objects {
		p.Objects = append(p.Objects, PublicObject{PublicID: object.PublicID, DisplayName: object.DisplayName, Size: object.Size})
	}
	return p, p.Validate()
}

func (p Projection) Validate() error {
	if p.Schema != ProjectionSchema || p.State != StateActive {
		return errors.New("public share projection schema or state is invalid")
	}
	if _, err := uuid.Parse(p.ChannelID); err != nil {
		return errors.New("public share projection channel_id must be UUID")
	}
	if _, err := slug.Path(p.Alias, p.Slug); err != nil {
		return err
	}
	if len(p.Recipients) > 0 && strings.TrimSpace(p.PasswordHash) != "" {
		return manifest.ErrClosedPassword
	}
	if p.PasswordHash != "" && manifest.ValidatePasswordVerifier(p.PasswordHash) != nil {
		return errors.New("public share projection password verifier is invalid")
	}
	if len(p.Recipients) > 256 || len(p.Objects) > 4096 {
		return errors.New("public share projection collection size is invalid")
	}
	if p.DoNotFollow != nil && *p.DoNotFollow < 1 {
		return errors.New("public share projection revision is invalid")
	}
	branding, err := realmbranding.Normalize(p.Branding)
	if err != nil || (p.Branding != (realmbranding.Branding{}) && branding != p.Branding) {
		return errors.New("public share projection branding is invalid or non-canonical")
	}
	seen := map[string]bool{}
	for _, object := range p.Objects {
		if seen[object.PublicID] || len(object.PublicID) < 16 || len(object.PublicID) > 64 || strings.TrimSpace(object.DisplayName) == "" || len(object.DisplayName) > 512 || strings.ContainsAny(object.DisplayName, "\x00\r\n") {
			return errors.New("public share projection object map is invalid")
		}
		if object.Size != nil && *object.Size < 0 {
			return errors.New("public share projection object size is invalid")
		}
		for _, character := range object.PublicID {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return errors.New("public share projection public id is invalid")
			}
		}
		seen[object.PublicID] = true
	}
	seenRecipients := map[string]bool{}
	for _, recipient := range p.Recipients {
		email := canonicalEmail(recipient.Email)
		at := strings.LastIndexByte(email, '@')
		if recipient.Email != email || len(email) > 254 || at <= 0 || at == len(email)-1 || strings.Count(email, "@") != 1 || strings.ContainsAny(email, "<>\r\n\t ,;") || seenRecipients[email] || len(recipient.TokenHash) != sha256.Size*2 {
			return errors.New("public share projection recipient is invalid")
		}
		if _, err := hex.DecodeString(recipient.TokenHash); err != nil {
			return errors.New("public share projection recipient hash is invalid")
		}
		seenRecipients[email] = true
	}
	return nil
}

func (s *Store) issueRecipients(channelID, epoch string, addresses []string, old []RecipientCredential) ([]RecipientCredential, []Delivery, error) {
	retained := map[string]RecipientCredential{}
	for _, credential := range old {
		retained[canonicalEmail(credential.Email)] = credential
	}
	seen := map[string]bool{}
	credentials := make([]RecipientCredential, 0, len(addresses))
	deliveries := []Delivery{}
	for _, raw := range addresses {
		email := canonicalEmail(raw)
		if seen[email] {
			return nil, nil, errors.New("public share recipient list contains duplicate address")
		}
		seen[email] = true
		if credential, ok := retained[email]; ok {
			credentials = append(credentials, credential)
			continue
		}
		token := s.recipientToken(channelID, epoch, email)
		digest := sha256.Sum256([]byte(token))
		credentials = append(credentials, RecipientCredential{Email: email, TokenHash: hex.EncodeToString(digest[:]), Epoch: epoch})
		deliveries = append(deliveries, Delivery{Email: email, Token: token})
	}
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].Email < credentials[j].Email })
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i].Email < deliveries[j].Email })
	return credentials, deliveries, nil
}

func (s *Store) deliveriesForEpoch(record Record, epoch string) []Delivery {
	deliveries := []Delivery{}
	for _, credential := range record.Recipients {
		if credential.Epoch == epoch {
			deliveries = append(deliveries, Delivery{Email: credential.Email, Token: s.recipientToken(record.ChannelID, epoch, credential.Email)})
		}
	}
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i].Email < deliveries[j].Email })
	return deliveries
}

func (s *Store) recipientToken(channelID, epoch, email string) string {
	mac := hmac.New(sha256.New, s.TokenKey)
	_, _ = mac.Write([]byte("filees public share recipient v1\x00" + channelID + "\x00" + epoch + "\x00" + email))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Store) validate() error {
	if !filepath.IsAbs(s.Root) || s.Authority == nil || len(s.TokenKey) < 32 || s.MaxChannelsPerRealm < 0 {
		return errors.New("public share store is incomplete")
	}
	return os.MkdirAll(filepath.Join(s.Root, "channels"), 0700)
}

func (s *Store) channelCount(ownerRealm string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "channels"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.loadPath(filepath.Join(s.Root, "channels", entry.Name()))
		if err != nil {
			return 0, err
		}
		if record.OwnerRealm == ownerRealm && record.State != StateDeleted {
			count++
		}
	}
	return count, nil
}

func (s *Store) load(channelID string) (Record, error) {
	if _, err := uuid.Parse(channelID); err != nil {
		return Record{}, ErrNotFound
	}
	return s.loadPath(s.recordPath(channelID))
}

func (s *Store) loadPath(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	var record Record
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, errors.New("stored public share channel is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("stored public share channel has trailing data")
	}
	if err := validateRecord(record); err != nil {
		return Record{}, errors.New("stored public share channel is invalid")
	}
	return record, nil
}

func validateRecord(record Record) error {
	if record.Schema != RecordSchema || record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return errors.New("record metadata is invalid")
	}
	for _, value := range []string{record.ChannelID, record.OwnerRealm, record.RepoID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != value {
			return errors.New("record identifier is invalid")
		}
	}
	if _, err := slug.Path(record.Alias, record.Slug); err != nil {
		return err
	}
	switch record.State {
	case StateActive:
		if record.Manifest == nil || record.DeletedAt != nil {
			return errors.New("active record lifecycle is invalid")
		}
	case StateRevoked:
		if record.Manifest == nil || record.RevokedAt == nil || record.DeletedAt != nil {
			return errors.New("revoked record lifecycle is invalid")
		}
	case StateDeleted:
		if record.Manifest != nil || len(record.Recipients) != 0 || record.DeletedAt == nil {
			return errors.New("deleted record retained private state")
		}
		return nil
	default:
		return errors.New("record state is invalid")
	}
	declaration := record.Manifest
	if declaration.OwnerRealm != record.OwnerRealm || declaration.RepoID != record.RepoID || declaration.Slug != record.Slug || declaration.Validate() != nil {
		return errors.New("record declaration conflicts with envelope")
	}
	if len(record.Recipients) != len(declaration.Recipients) {
		return errors.New("record credential count conflicts with recipients")
	}
	want := map[string]bool{}
	for _, email := range declaration.Recipients {
		want[canonicalEmail(email)] = true
	}
	for _, credential := range record.Recipients {
		if !want[credential.Email] || credential.Email != canonicalEmail(credential.Email) || len(credential.TokenHash) != sha256.Size*2 {
			return errors.New("record recipient credential is invalid")
		}
		if _, err := hex.DecodeString(credential.TokenHash); err != nil {
			return errors.New("record recipient digest is invalid")
		}
		if parsed, err := uuid.Parse(credential.Epoch); err != nil || parsed.String() != credential.Epoch {
			return errors.New("record recipient epoch is invalid")
		}
		delete(want, credential.Email)
	}
	if len(want) != 0 {
		return errors.New("record recipient credentials are incomplete")
	}
	return nil
}

func (s *Store) recordPath(channelID string) string {
	return filepath.Join(s.Root, "channels", channelID+".json")
}
func (s *Store) slugPath(ownerRealm, value string) string {
	return filepath.Join(s.Root, "slugs", ownerRealm, value+".json")
}
func (s *Store) addressPath(alias, value string) string {
	return filepath.Join(s.Root, "addresses", alias, value+".json")
}
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type slugReservation struct {
	Schema     string    `json:"schema"`
	OwnerRealm string    `json:"owner_realm"`
	Alias      string    `json:"alias"`
	Slug       string    `json:"slug"`
	ChannelID  string    `json:"channel_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) ensureReservation(want slugReservation) error {
	paths := []string{s.slugPath(want.OwnerRealm, want.Slug), s.addressPath(want.Alias, want.Slug)}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err == nil {
			var old slugReservation
			if json.Unmarshal(raw, &old) != nil || old.Schema != SlugSchema || old.ChannelID != want.ChannelID || old.OwnerRealm != want.OwnerRealm || old.Alias != want.Alias || old.Slug != want.Slug {
				return ErrSlugTaken
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := atomicJSON(path, 0600, want); err != nil {
			return err
		}
	}
	return nil
}

func canonicalEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func sameDeclaration(a, b manifest.Share) bool {
	rawA, _ := json.Marshal(a)
	rawB, _ := json.Marshal(b)
	return hmac.Equal(rawA, rawB)
}

func atomicJSON(path string, mode os.FileMode, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".public-share-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (r Record) String() string { return fmt.Sprintf("%s/%s (%s)", r.Alias, r.ChannelID, r.State) }
