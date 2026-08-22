package channel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/realmbranding"
	"filees/public-shares/manifest"
	"filees/public-shares/slug"
	"github.com/google/uuid"
)

const (
	UploadRecordSchema = "filees.upload-channel/v1"
	TrashRepoName      = "filees-upload-trash"
)

// UploadRecord is the authoritative upload-channel lifecycle. It lives beside
// public-share records so both machines share slug/address tombstones without
// sharing a manifest type.
type UploadRecord struct {
	Schema     string                `json:"schema"`
	ChannelID  string                `json:"channel_id"`
	OwnerRealm string                `json:"owner_realm"`
	Alias      string                `json:"alias"`
	Slug       string                `json:"slug"`
	State      string                `json:"state"`
	Manifest   *manifest.Upload      `json:"manifest,omitempty"`
	Recipients []RecipientCredential `json:"recipient_credentials,omitempty"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
	RevokedAt  *time.Time            `json:"revoked_at,omitempty"`
	DeletedAt  *time.Time            `json:"deleted_at,omitempty"`
}

// ReserveAddress binds alias/slug to channelID under the shared Public Shares
// tombstone. Upload and download cannot occupy the same public path.
func (s *Store) ReserveAddress(channelID, ownerRealm, alias, channelSlug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return err
	}
	if _, err := uuid.Parse(channelID); err != nil {
		return errors.New("upload channel operation_id must be UUID")
	}
	if _, err := slug.Path(alias, channelSlug); err != nil {
		return err
	}
	return s.ensureReservation(slugReservation{Schema: SlugSchema, OwnerRealm: ownerRealm, Alias: alias, Slug: channelSlug, ChannelID: channelID, CreatedAt: s.now()})
}

func (s *Store) CreateUpload(operationID, requesterRealm string, declaration manifest.Upload) (UploadRecord, []Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return UploadRecord{}, nil, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return UploadRecord{}, nil, errors.New("upload channel operation_id must be UUID")
	}
	if declaration.OwnerRealm != requesterRealm {
		return UploadRecord{}, nil, ErrForbidden
	}
	if declaration.CollisionPolicy == "" {
		declaration.CollisionPolicy = manifest.CollisionDeny
	}
	if declaration.CollisionPolicy != manifest.CollisionDeny {
		return UploadRecord{}, nil, ErrPolicy
	}
	if err := declaration.Validate(); err != nil {
		return UploadRecord{}, nil, err
	}
	if err := s.Authority.OwnsActiveRepository(requesterRealm, declaration.AuthorityRepoID); err != nil {
		return UploadRecord{}, nil, ErrForbidden
	}
	alias, err := s.Authority.ActiveRealmAlias(requesterRealm)
	if err != nil {
		return UploadRecord{}, nil, ErrForbidden
	}
	alias = strings.TrimSpace(alias)
	if _, err := slug.Path(alias, declaration.Slug); err != nil {
		return UploadRecord{}, nil, err
	}
	path := s.uploadRecordPath(operationID)
	if old, err := s.loadUploadPath(path); err == nil {
		if old.Manifest != nil && old.State == StateActive && sameUpload(*old.Manifest, declaration) {
			return old, s.uploadDeliveriesForEpoch(old, operationID), nil
		}
		return UploadRecord{}, nil, ErrRecordConflict
	} else if !errors.Is(err, ErrNotFound) {
		return UploadRecord{}, nil, err
	}
	reservation := slugReservation{Schema: SlugSchema, OwnerRealm: requesterRealm, Alias: alias, Slug: declaration.Slug, ChannelID: operationID, CreatedAt: s.now()}
	if err := s.ensureReservation(reservation); err != nil {
		return UploadRecord{}, nil, err
	}
	recipients, deliveries, err := s.issueUploadRecipients(operationID, operationID, declaration.Recipients, nil)
	if err != nil {
		return UploadRecord{}, nil, err
	}
	now := s.now()
	record := UploadRecord{Schema: UploadRecordSchema, ChannelID: operationID, OwnerRealm: requesterRealm, Alias: alias, Slug: declaration.Slug, State: StateActive, Manifest: &declaration, Recipients: recipients, CreatedAt: now, UpdatedAt: now}
	if err := atomicJSON(path, 0600, record); err != nil {
		return UploadRecord{}, nil, err
	}
	return record, deliveries, nil
}

func (s *Store) UpdateUpload(operationID, requesterRealm, channelID string, declaration manifest.Upload) (UploadRecord, []Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return UploadRecord{}, nil, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return UploadRecord{}, nil, errors.New("upload channel operation_id must be UUID")
	}
	record, err := s.loadUpload(channelID)
	if err != nil {
		return UploadRecord{}, nil, err
	}
	if record.Manifest == nil || record.Manifest.OwnerRealm != requesterRealm {
		return UploadRecord{}, nil, ErrForbidden
	}
	if record.State != StateActive {
		return UploadRecord{}, nil, ErrInactive
	}
	if declaration.CollisionPolicy == "" {
		declaration.CollisionPolicy = manifest.CollisionDeny
	}
	if declaration.OwnerRealm != requesterRealm || declaration.AuthorityRepoID != record.Manifest.AuthorityRepoID || declaration.UploadRepoID != record.Manifest.UploadRepoID || declaration.Slug != record.Manifest.Slug {
		return UploadRecord{}, nil, ErrRecordConflict
	}
	if declaration.CollisionPolicy != manifest.CollisionDeny {
		return UploadRecord{}, nil, ErrPolicy
	}
	if err := declaration.Validate(); err != nil {
		return UploadRecord{}, nil, err
	}
	if err := s.Authority.OwnsActiveRepository(requesterRealm, declaration.AuthorityRepoID); err != nil {
		return UploadRecord{}, nil, ErrForbidden
	}
	if sameUpload(*record.Manifest, declaration) {
		return record, s.uploadDeliveriesForEpoch(record, operationID), nil
	}
	recipients, deliveries, err := s.issueUploadRecipients(channelID, operationID, declaration.Recipients, record.Recipients)
	if err != nil {
		return UploadRecord{}, nil, err
	}
	record.Manifest = &declaration
	record.Recipients = recipients
	record.UpdatedAt = s.now()
	if err := atomicJSON(s.uploadRecordPath(channelID), 0600, record); err != nil {
		return UploadRecord{}, nil, err
	}
	return record, deliveries, nil
}

func (s *Store) ListOwnedUploads(requesterRealm, authorityRepoID string) ([]UploadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(requesterRealm); err != nil {
		return nil, ErrForbidden
	}
	if _, err := uuid.Parse(authorityRepoID); err != nil {
		return nil, ErrNotFound
	}
	if err := s.Authority.OwnsActiveRepository(requesterRealm, authorityRepoID); err != nil {
		return nil, ErrForbidden
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "upload-channels"))
	if errors.Is(err, os.ErrNotExist) {
		return []UploadRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]UploadRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.loadUploadPath(filepath.Join(s.Root, "upload-channels", entry.Name()))
		if err != nil {
			return nil, err
		}
		if record.OwnerRealm == requesterRealm && record.Manifest != nil && record.Manifest.AuthorityRepoID == authorityRepoID && record.State != StateDeleted {
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

func (s *Store) RevokeUpload(requesterRealm, channelID string) (UploadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.loadUpload(channelID)
	if err != nil {
		return UploadRecord{}, err
	}
	if record.Manifest == nil || record.Manifest.OwnerRealm != requesterRealm {
		return UploadRecord{}, ErrForbidden
	}
	if record.State == StateDeleted {
		return UploadRecord{}, ErrNotFound
	}
	if record.State == StateRevoked {
		return record, nil
	}
	now := s.now()
	record.State, record.UpdatedAt, record.RevokedAt = StateRevoked, now, &now
	if err := atomicJSON(s.uploadRecordPath(channelID), 0600, record); err != nil {
		return UploadRecord{}, err
	}
	return record, nil
}

func (s *Store) DeleteUpload(requesterRealm, channelID string) (UploadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.loadUpload(channelID)
	if err != nil {
		return UploadRecord{}, err
	}
	if record.Manifest == nil {
		if record.State == StateDeleted {
			return record, nil
		}
		return UploadRecord{}, ErrForbidden
	}
	if record.Manifest.OwnerRealm != requesterRealm {
		return UploadRecord{}, ErrForbidden
	}
	now := s.now()
	record.State, record.UpdatedAt, record.DeletedAt = StateDeleted, now, &now
	record.Manifest, record.Recipients = nil, nil
	if err := atomicJSON(s.uploadRecordPath(channelID), 0600, record); err != nil {
		return UploadRecord{}, err
	}
	return record, nil
}

func (s *Store) GetUpload(channelID string) (UploadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUpload(channelID)
}

const UploadProjectionSchema = "filees.upload-channel-projection/v1"

// UploadProjection is all the public FastCGI process may know. Repository
// IDs, mailbox addresses and the upload target never appear here.
type UploadProjection struct {
	Schema     string                 `json:"schema"`
	ChannelID  string                 `json:"channel_id"`
	Alias      string                 `json:"alias"`
	Slug       string                 `json:"slug"`
	State      string                 `json:"state"`
	RequireOTP bool                   `json:"require_otp,omitempty"`
	Recipients []PublicRecipient      `json:"recipients"`
	Branding   realmbranding.Branding `json:"branding"`
	UpdatedAt  time.Time              `json:"updated_at"`
}

func (s *Store) ResolveUploadAddress(alias, channelSlug string) (UploadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := slug.Path(alias, channelSlug); err != nil {
		return UploadRecord{}, ErrNotFound
	}
	raw, err := os.ReadFile(s.addressPath(alias, channelSlug))
	if err != nil {
		return UploadRecord{}, ErrNotFound
	}
	var reservation slugReservation
	if json.Unmarshal(raw, &reservation) != nil || reservation.Schema != SlugSchema || reservation.Alias != alias || reservation.Slug != channelSlug {
		return UploadRecord{}, ErrNotFound
	}
	record, err := s.loadUpload(reservation.ChannelID)
	if err != nil || record.State != StateActive || record.Manifest == nil || record.Alias != alias || record.Slug != channelSlug || record.Manifest.Slug != channelSlug {
		return UploadRecord{}, ErrNotFound
	}
	return record, nil
}

func (s *Store) UploadProjection(channelID string) (UploadProjection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.loadUpload(channelID)
	if err != nil {
		return UploadProjection{}, err
	}
	branding, err := s.Authority.ActiveRealmBranding(record.OwnerRealm)
	if err != nil {
		return UploadProjection{}, ErrNotFound
	}
	return projectUpload(record, branding)
}

func projectUpload(record UploadRecord, branding realmbranding.Branding) (UploadProjection, error) {
	if record.State != StateActive || record.Manifest == nil {
		return UploadProjection{}, ErrNotFound
	}
	p := UploadProjection{Schema: UploadProjectionSchema, ChannelID: record.ChannelID, Alias: record.Alias, Slug: record.Slug, State: record.State, RequireOTP: record.Manifest.RequireOTP, Branding: branding, UpdatedAt: record.UpdatedAt}
	for _, recipient := range record.Recipients {
		p.Recipients = append(p.Recipients, PublicRecipient{InvitationHash: recipient.TokenHash})
	}
	return p, p.Validate()
}

func (p UploadProjection) Validate() error {
	if p.Schema != UploadProjectionSchema || p.State != StateActive {
		return errors.New("upload channel projection schema or state is invalid")
	}
	if _, err := uuid.Parse(p.ChannelID); err != nil {
		return errors.New("upload channel projection channel_id must be UUID")
	}
	if _, err := slug.Path(p.Alias, p.Slug); err != nil {
		return err
	}
	if len(p.Recipients) == 0 || len(p.Recipients) > 256 {
		return errors.New("upload channel projection recipient list is invalid")
	}
	branding, err := realmbranding.Normalize(p.Branding)
	if err != nil || (p.Branding != (realmbranding.Branding{}) && branding != p.Branding) {
		return errors.New("upload channel projection branding is invalid or non-canonical")
	}
	seen := map[string]bool{}
	for _, recipient := range p.Recipients {
		if seen[recipient.InvitationHash] || len(recipient.InvitationHash) != sha256.Size*2 {
			return errors.New("upload channel projection recipient is invalid")
		}
		if _, err := hex.DecodeString(recipient.InvitationHash); err != nil {
			return errors.New("upload channel projection recipient hash is invalid")
		}
		seen[recipient.InvitationHash] = true
	}
	return nil
}

func (s *Store) loadUpload(channelID string) (UploadRecord, error) {
	if _, err := uuid.Parse(channelID); err != nil {
		return UploadRecord{}, ErrNotFound
	}
	return s.loadUploadPath(s.uploadRecordPath(channelID))
}

func (s *Store) loadUploadPath(path string) (UploadRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return UploadRecord{}, ErrNotFound
	}
	if err != nil {
		return UploadRecord{}, err
	}
	var record UploadRecord
	if json.Unmarshal(raw, &record) != nil || record.Schema != UploadRecordSchema || record.ChannelID == "" {
		return UploadRecord{}, errors.New("upload channel record is invalid")
	}
	return record, nil
}

func (s *Store) uploadRecordPath(channelID string) string {
	return filepath.Join(s.Root, "upload-channels", channelID+".json")
}

func (s *Store) issueUploadRecipients(channelID, epoch string, addresses []string, old []RecipientCredential) ([]RecipientCredential, []Delivery, error) {
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
			return nil, nil, errors.New("upload channel recipient list contains duplicate address")
		}
		seen[email] = true
		if credential, ok := retained[email]; ok {
			credentials = append(credentials, credential)
			continue
		}
		token := s.uploadRecipientToken(channelID, epoch, email)
		digest := sha256.Sum256([]byte(token))
		credentials = append(credentials, RecipientCredential{Email: email, TokenHash: hex.EncodeToString(digest[:]), Epoch: epoch})
		deliveries = append(deliveries, Delivery{Email: email, Token: token})
	}
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].Email < credentials[j].Email })
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i].Email < deliveries[j].Email })
	return credentials, deliveries, nil
}

func (s *Store) uploadDeliveriesForEpoch(record UploadRecord, epoch string) []Delivery {
	deliveries := []Delivery{}
	for _, credential := range record.Recipients {
		if credential.Epoch == epoch {
			deliveries = append(deliveries, Delivery{Email: credential.Email, Token: s.uploadRecipientToken(record.ChannelID, epoch, credential.Email)})
		}
	}
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i].Email < deliveries[j].Email })
	return deliveries
}

func (s *Store) uploadRecipientToken(channelID, epoch, email string) string {
	mac := hmac.New(sha256.New, s.TokenKey)
	_, _ = mac.Write([]byte("filees upload channel recipient v1\x00" + channelID + "\x00" + epoch + "\x00" + email))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func sameUpload(a, b manifest.Upload) bool {
	rawA, _ := json.Marshal(a)
	rawB, _ := json.Marshal(b)
	return hmac.Equal(rawA, rawB)
}
