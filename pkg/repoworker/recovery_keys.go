package repoworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const RecoveryKeySchema = "filees.realm-recovery-key/v1"

// RecoveryKeyRecord is a server-side public capability bound to one immutable
// recovery manifest. It contains no private key and cannot activate a client.
type RecoveryKeyRecord struct {
	Schema        string    `json:"schema"`
	OperationID   string    `json:"operation_id"`
	RealmID       string    `json:"realm_id"`
	PublicKey     string    `json:"public_key"`
	DownloadUntil time.Time `json:"download_until"`
	CreatedAt     time.Time `json:"created_at"`
}

type RecoveryKeyStore struct{ Root string }

func (s RecoveryKeyStore) Bind(manifest RecoveryManifest, publicKey string) (RecoveryKeyRecord, error) {
	if !filepath.IsAbs(s.Root) {
		return RecoveryKeyRecord{}, errors.New("recovery key root must be absolute")
	}
	if err := validateRecoveryManifest(manifest); err != nil {
		return RecoveryKeyRecord{}, err
	}
	key, err := normalizeRecoveryPublicKey(publicKey)
	if err != nil {
		return RecoveryKeyRecord{}, err
	}
	record := RecoveryKeyRecord{Schema: RecoveryKeySchema, OperationID: manifest.OperationID, RealmID: manifest.RealmID, PublicKey: key, DownloadUntil: manifest.DownloadUntil, CreatedAt: manifest.CreatedAt}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return RecoveryKeyRecord{}, err
	}
	err = WithFileLock(filepath.Join(s.Root, ".recovery-key.lock"), func() error {
		path := s.path(record.OperationID)
		if raw, err := os.ReadFile(path); err == nil {
			var old RecoveryKeyRecord
			if json.Unmarshal(raw, &old) != nil || validateRecoveryKey(old) != nil {
				return errors.New("stored recovery key is invalid")
			}
			oldRaw, _ := json.Marshal(old)
			newRaw, _ := json.Marshal(record)
			if !bytes.Equal(oldRaw, newRaw) {
				return errors.New("recovery key conflicts with prior receipt")
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return atomicJSON(path, record)
	})
	return record, err
}

func (s RecoveryKeyStore) FindByPublicKey(publicKey string, now time.Time) (RecoveryKeyRecord, error) {
	if !filepath.IsAbs(s.Root) {
		return RecoveryKeyRecord{}, errors.New("recovery key root must be absolute")
	}
	key, err := normalizeRecoveryPublicKey(publicKey)
	if err != nil {
		return RecoveryKeyRecord{}, err
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return RecoveryKeyRecord{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.Root, entry.Name()))
		if err != nil {
			return RecoveryKeyRecord{}, err
		}
		var record RecoveryKeyRecord
		if json.Unmarshal(raw, &record) != nil || validateRecoveryKey(record) != nil {
			return RecoveryKeyRecord{}, errors.New("stored recovery key is invalid")
		}
		if record.PublicKey == key {
			if !now.UTC().Before(record.DownloadUntil) {
				return RecoveryKeyRecord{}, errors.New("recovery key expired")
			}
			return record, nil
		}
	}
	return RecoveryKeyRecord{}, os.ErrNotExist
}

func (s RecoveryKeyStore) path(operationID string) string {
	return filepath.Join(s.Root, operationID+".json")
}

// Remove deletes the public capability only after the manifest reaper has
// crossed grace. It is deliberately operation-specific and idempotent.
func (s RecoveryKeyStore) Remove(operationID string) error {
	if !filepath.IsAbs(s.Root) {
		return errors.New("recovery key root must be absolute")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return errors.New("recovery key operation_id must be UUID")
	}
	return WithFileLock(filepath.Join(s.Root, ".recovery-key.lock"), func() error {
		if err := os.Remove(s.path(operationID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func normalizeRecoveryPublicKey(value string) (string, error) {
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(value)))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || comment != "" {
		return "", errors.New("recovery public key must be one comment-free Ed25519 key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}

func validateRecoveryKey(record RecoveryKeyRecord) error {
	if record.Schema != RecoveryKeySchema {
		return errors.New("recovery key schema is invalid")
	}
	if _, err := uuid.Parse(record.OperationID); err != nil {
		return errors.New("recovery key operation_id must be UUID")
	}
	if _, err := uuid.Parse(record.RealmID); err != nil || record.CreatedAt.IsZero() || !record.DownloadUntil.After(record.CreatedAt) {
		return errors.New("recovery key retention is invalid")
	}
	_, err := normalizeRecoveryPublicKey(record.PublicKey)
	return err
}
