package repoworker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

const RecoveryManifestSchema = "filees.realm-recovery-manifest/v1"

// RecoveryArchive is a capability-neutral description of exactly one dump.
// ArchiveID is opaque and is never interpreted as a filesystem path.
type RecoveryArchive struct {
	ArchiveID string `json:"archive_id"`
	RepoID    string `json:"repo_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type RecoveryManifest struct {
	Schema          string            `json:"schema"`
	OperationID     string            `json:"operation_id"`
	RealmID         string            `json:"realm_id"`
	Archives        []RecoveryArchive `json:"archives"`
	DownloadUntil   time.Time         `json:"download_until"`
	AdminGraceUntil time.Time         `json:"admin_grace_until"`
	CreatedAt       time.Time         `json:"created_at"`
}

// RecoveryManifestStore stores capability-neutral receipts. The matching
// private recovery key is deliberately outside this server-side store.
type RecoveryManifestStore struct{ Root string }

func (s RecoveryManifestStore) Save(manifest RecoveryManifest) error {
	if !filepath.IsAbs(s.Root) {
		return errors.New("recovery manifest root must be absolute")
	}
	if err := validateRecoveryManifest(manifest); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	return WithFileLock(filepath.Join(s.Root, ".recovery-manifest.lock"), func() error {
		path := s.path(manifest.OperationID)
		if raw, err := os.ReadFile(path); err == nil {
			var old RecoveryManifest
			if json.Unmarshal(raw, &old) != nil || validateRecoveryManifest(old) != nil {
				return errors.New("stored recovery manifest is invalid")
			}
			oldRaw, _ := json.Marshal(old)
			newRaw, _ := json.Marshal(manifest)
			if string(oldRaw) != string(newRaw) {
				return errors.New("recovery manifest conflicts with prior receipt")
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return atomicJSON(path, manifest)
	})
}

func (s RecoveryManifestStore) Load(operationID string) (RecoveryManifest, error) {
	if !filepath.IsAbs(s.Root) {
		return RecoveryManifest{}, errors.New("recovery manifest root must be absolute")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return RecoveryManifest{}, errors.New("recovery manifest operation_id must be UUID")
	}
	raw, err := os.ReadFile(s.path(operationID))
	if err != nil {
		return RecoveryManifest{}, err
	}
	var manifest RecoveryManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return RecoveryManifest{}, err
	}
	if err := validateRecoveryManifest(manifest); err != nil || manifest.OperationID != operationID {
		return RecoveryManifest{}, errors.New("recovery manifest is invalid")
	}
	return manifest, nil
}

func (s RecoveryManifestStore) path(operationID string) string {
	return filepath.Join(s.Root, operationID+".json")
}

// ReapExpired removes only receipts whose manual-contact grace period ended.
// It is intentionally an explicit call from another server action, never a
// resident or scheduled worker.
func (s RecoveryManifestStore) ReapExpired(now time.Time) ([]string, error) {
	expired, err := s.Expired(now)
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0, len(expired))
	for _, operationID := range expired {
		if err := s.RemoveExpired(operationID, now); err != nil {
			return removed, err
		}
		removed = append(removed, operationID)
	}
	return removed, nil
}

func (s RecoveryManifestStore) Expired(now time.Time) ([]string, error) {
	if !filepath.IsAbs(s.Root) {
		return nil, errors.New("recovery manifest root must be absolute")
	}
	if _, err := os.Stat(s.Root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var expired []string
	err := WithFileLock(filepath.Join(s.Root, ".recovery-manifest.lock"), func() error {
		entries, err := os.ReadDir(s.Root)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			path := filepath.Join(s.Root, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var manifest RecoveryManifest
			if json.Unmarshal(raw, &manifest) != nil || validateRecoveryManifest(manifest) != nil {
				return errors.New("stored recovery manifest is invalid")
			}
			if now.UTC().Before(manifest.AdminGraceUntil) {
				continue
			}
			expired = append(expired, manifest.OperationID)
		}
		return nil
	})
	return expired, err
}

func (s RecoveryManifestStore) RemoveExpired(operationID string, now time.Time) error {
	manifest, err := s.Load(operationID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if now.UTC().Before(manifest.AdminGraceUntil) {
		return errors.New("recovery manifest grace has not expired")
	}
	return WithFileLock(filepath.Join(s.Root, ".recovery-manifest.lock"), func() error {
		current, err := s.Load(operationID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if now.UTC().Before(current.AdminGraceUntil) {
			return errors.New("recovery manifest grace has not expired")
		}
		if err := os.Remove(s.path(operationID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(s.Root)
	})
}

func validateRecoveryManifest(m RecoveryManifest) error {
	if m.Schema != RecoveryManifestSchema {
		return errors.New("recovery manifest schema is invalid")
	}
	if _, err := uuid.Parse(m.OperationID); err != nil {
		return errors.New("recovery manifest operation_id must be UUID")
	}
	if _, err := uuid.Parse(m.RealmID); err != nil {
		return errors.New("recovery manifest realm_id must be UUID")
	}
	if m.CreatedAt.IsZero() || m.DownloadUntil.Before(m.CreatedAt) || m.AdminGraceUntil.Before(m.DownloadUntil) {
		return errors.New("recovery manifest retention is invalid")
	}
	seen := map[string]bool{}
	for _, archive := range m.Archives {
		if _, err := uuid.Parse(archive.ArchiveID); err != nil {
			return errors.New("recovery archive_id must be UUID")
		}
		if _, err := uuid.Parse(archive.RepoID); err != nil || seen[archive.ArchiveID] || archive.Size < 0 || len(archive.SHA256) != sha256.Size*2 {
			return errors.New("recovery archive is invalid")
		}
		if _, err := hex.DecodeString(archive.SHA256); err != nil {
			return errors.New("recovery archive digest is invalid")
		}
		seen[archive.ArchiveID] = true
	}
	return nil
}

func SortedRecoveryArchives(archives []RecoveryArchive) []RecoveryArchive {
	out := append([]RecoveryArchive(nil), archives...)
	sort.Slice(out, func(i, j int) bool { return out[i].ArchiveID < out[j].ArchiveID })
	return out
}
