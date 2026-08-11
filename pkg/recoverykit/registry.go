package recoverykit

import (
	"encoding/json"
	"errors"
	"filees/internal/durable"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

const RegistrySchema = "filees.recovery-registry/v1"

type RegistryEntry struct {
	Schema          string    `json:"schema"`
	OperationID     string    `json:"operation_id"`
	ServerID        string    `json:"server_id"`
	ServerName      string    `json:"server_name"`
	KitPath         string    `json:"kit_path"`
	AdminContact    string    `json:"admin_contact,omitempty"`
	ArchiveCount    int       `json:"archive_count"`
	DownloadUntil   time.Time `json:"download_until"`
	AdminGraceUntil time.Time `json:"admin_grace_until"`
}

type Registry struct{ Root string }

func (r Registry) Put(entry RegistryEntry) error {
	if err := r.validate(); err != nil {
		return err
	}
	if err := validateRegistryEntry(entry); err != nil {
		return err
	}
	if err := os.MkdirAll(r.Root, 0o700); err != nil {
		return err
	}
	path := filepath.Join(r.Root, entry.OperationID+".json")
	if raw, err := os.ReadFile(path); err == nil {
		var old RegistryEntry
		if json.Unmarshal(raw, &old) != nil || validateRegistryEntry(old) != nil {
			return errors.New("stored recovery registry entry is invalid")
		}
		oldRaw, _ := json.Marshal(old)
		newRaw, _ := json.Marshal(entry)
		if string(oldRaw) != string(newRaw) {
			return errors.New("recovery registry entry conflicts with prior receipt")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return storePrivateFile(path, append(raw, '\n'))
}

func (r Registry) List(now time.Time) ([]RegistryEntry, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(r.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []RegistryEntry
	for _, file := range entries {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		path := filepath.Join(r.Root, file.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var entry RegistryEntry
		if json.Unmarshal(raw, &entry) != nil || validateRegistryEntry(entry) != nil || file.Name() != entry.OperationID+".json" {
			return nil, errors.New("stored recovery registry entry is invalid")
		}
		if !now.UTC().Before(entry.AdminGraceUntil) {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			continue
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AdminGraceUntil.Before(result[j].AdminGraceUntil) })
	return result, nil
}

func (r Registry) Find(operationID string, now time.Time) (RegistryEntry, error) {
	if _, err := uuid.Parse(operationID); err != nil {
		return RegistryEntry{}, errors.New("recovery operation ID must be UUID")
	}
	entries, err := r.List(now)
	if err != nil {
		return RegistryEntry{}, err
	}
	for _, entry := range entries {
		if entry.OperationID == operationID {
			return entry, nil
		}
	}
	return RegistryEntry{}, os.ErrNotExist
}

func (r Registry) validate() error {
	if !filepath.IsAbs(r.Root) {
		return errors.New("recovery registry root must be absolute")
	}
	return nil
}

func validateRegistryEntry(entry RegistryEntry) error {
	if entry.Schema != RegistrySchema {
		return errors.New("recovery registry schema is invalid")
	}
	if _, err := uuid.Parse(entry.OperationID); err != nil {
		return errors.New("recovery registry operation ID must be UUID")
	}
	if entry.ServerID == "" || entry.ServerName == "" || !filepath.IsAbs(entry.KitPath) || entry.ArchiveCount < 0 || entry.DownloadUntil.IsZero() || entry.AdminGraceUntil.Before(entry.DownloadUntil) {
		return errors.New("recovery registry entry is invalid")
	}
	return nil
}

func storePrivateFile(path string, raw []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".recovery-registry-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err == nil {
		_, err = temp.Write(raw)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	// The registry lives under the client profile root, so this is a Windows
	// path in production; os.Open+Sync would fail there with "Access is
	// denied".
	return durable.SyncDirectory(filepath.Dir(path))
}
