package repoworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

type FileStore struct {
	root string
	mu   sync.Mutex
}

const operationBindingSchema = "filees.repository-operation-binding/v1"

type operationBinding struct {
	Schema        string             `json:"schema"`
	OperationID   string             `json:"operation_id"`
	RequestID     string             `json:"request_id"`
	Type          control.TicketType `json:"type"`
	ClientID      string             `json:"client_id"`
	RealmID       string             `json:"realm_id"`
	PayloadSHA256 string             `json:"payload_sha256"`
}

// Bind is persisted before the domain action. It prevents another authenticated
// actor from replaying a result, including a pairing token. A result left by an
// older build without a binding is refused, never adopted or re-executed.
func (s *FileStore) Bind(ticket control.Ticket, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resultPath, err := s.path(ticket.OperationID, ticket.Type)
	if err != nil {
		return err
	}
	canonical, err := canonicalPayload(ticket.Payload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	want := operationBinding{
		Schema: operationBindingSchema, OperationID: ticket.OperationID,
		RequestID: ticket.RequestID, Type: ticket.Type,
		ClientID: session.ClientID, RealmID: session.RealmID,
		PayloadSHA256: hex.EncodeToString(digest[:]),
	}
	path := resultPath + ".binding"
	raw, err := os.ReadFile(path)
	if err == nil {
		var have operationBinding
		if json.Unmarshal(raw, &have) != nil || have != want {
			return errors.New("operation is bound to another authenticated request")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(resultPath); err == nil {
		return errors.New("legacy operation result has no authenticated binding")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err = json.MarshalIndent(want, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.root, ".binding-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := syncDirectory(s.root); err != nil {
		return fmt.Errorf("persist operation binding: %w", err)
	}
	return nil
}

func canonicalPayload(raw json.RawMessage) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("operation payload contains trailing data")
	}
	return json.Marshal(value)
}

func NewFileStore(root string) (*FileStore, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("repository result root must be absolute")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}
func (s *FileStore) path(id string, typ control.TicketType) (string, error) {
	if _, e := uuid.Parse(id); e != nil {
		return "", errors.New("operation ID must be UUID")
	}
	if typ != control.TicketStoragePreflight && typ != control.TicketCreateRepository && typ != control.TicketInitialCommit && typ != control.TicketDeleteRepository && typ != control.TicketPrepareRepositoryRecovery && typ != control.TicketMobilePairing && typ != control.TicketClaimRealmAlias && typ != control.TicketResolveOwnerLabels && typ != control.TicketClientDeactivate && typ != control.TicketRealmRemoveRequest && typ != control.TicketRealmRemoveConfirm && typ != control.TicketLoadRepositoryDump && typ != control.TicketGrantAccess && typ != control.TicketRevokeAccess && typ != control.TicketListGrantRecipients && typ != control.TicketSetRealmVisibility && typ != control.TicketGetRealmPublicBranding && typ != control.TicketSetRealmPublicBranding && typ != control.TicketListPublicShares && typ != control.TicketCreatePublicShare && typ != control.TicketUpdatePublicShare && typ != control.TicketRevokePublicShare && typ != control.TicketDeletePublicShare && typ != control.TicketListUploadChannels && typ != control.TicketCreateUploadChannel && typ != control.TicketUpdateUploadChannel && typ != control.TicketRevokeUploadChannel && typ != control.TicketDeleteUploadChannel && typ != control.TicketListQuarantine && typ != control.TicketHideQuarantine && typ != control.TicketFetchQuarantine && typ != control.TicketListShelf && typ != control.TicketGetGUIBlob && typ != control.TicketSetGUIBlob {
		return "", errors.New("unsupported result ticket type")
	}
	return filepath.Join(s.root, id+"."+strings.ToLower(string(typ))+".json"), nil
}
func (s *FileStore) Load(id string, typ control.TicketType) (control.Result, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, e := s.path(id, typ)
	if e != nil {
		return control.Result{}, false, e
	}
	raw, e := os.ReadFile(p)
	if errors.Is(e, os.ErrNotExist) {
		return control.Result{}, false, nil
	}
	if e != nil {
		return control.Result{}, false, e
	}
	r, e := control.ParseResult(raw)
	return r, e == nil, e
}
func (s *FileStore) Save(r control.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := r.Validate(); e != nil {
		return e
	}
	p, e := s.path(r.OperationID, r.Type)
	if e != nil {
		return e
	}
	if raw, e := os.ReadFile(p); e == nil {
		old, e := control.ParseResult(raw)
		if e != nil {
			return e
		}
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(r)
		if string(a) == string(b) {
			return nil
		}
		return errors.New("conflicting result for operation")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	raw, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(s.root, ".result-*.tmp")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(append(raw, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, p); e != nil {
		return e
	}
	return syncDirectory(s.root)
}

// PurgeExpiredMobilePairings deletes MOBILE_PAIRING result files whose
// MobilePairingResult.ExpiresAt has passed. A stored result is only ever
// re-read to make a retried control-plane request idempotent (Worker.Handle
// returns it unchanged for a matching operation_id+request_id) - once the
// token's own TTL has passed it can never authenticate again
// (onboarding.Files.AuthenticateOTP is TTL-gated on the same clock), so no
// legitimate retry can still need it. Deletes rather than redacts, matching
// this codebase's existing expire-and-remove convention
// (activation.Manager.expireStagedLocked, onboarding.Files's own
// expireOperationsLocked) - nothing in this package is treated as a
// long-term audit log today.
func (s *FileStore) PurgeExpiredMobilePairings(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pattern := filepath.Join(s.root, "*."+strings.ToLower(string(control.TicketMobilePairing))+".json")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return purged, err
		}
		result, err := control.ParseResult(raw)
		if err != nil {
			return purged, err
		}
		if result.Status != control.ResultOK {
			continue
		}
		var payload control.MobilePairingResult
		if err := control.DecodePayload(result.Result, &payload); err != nil {
			return purged, err
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, payload.ExpiresAt)
		if err != nil {
			return purged, err
		}
		if !now.After(expiresAt) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return purged, err
		}
		if err := os.Remove(path + ".binding"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return purged, err
		}
		purged++
	}
	return purged, nil
}
