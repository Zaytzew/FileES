package whaleworker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"filees/pkg/repoworker"
	whale "filees/pkg/whale/v1"
)

type BusyError struct{ Position int }

func (BusyError) Error() string { return "Whale path is busy" }

type PathQueue struct{ Root string }

type queueRecord struct {
	Schema        string   `json:"schema"`
	LogicalRepoID string   `json:"logical_repo_id"`
	LogicalPath   string   `json:"logical_path"`
	Holder        string   `json:"holder"`
	Waiting       []string `json:"waiting"`
}

func (q PathQueue) Claim(identity whale.Identity) (int, error) {
	if !filepath.IsAbs(q.Root) {
		return 0, errors.New("Whale queue root must be absolute")
	}
	if err := identity.Validate(); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(q.Root, 0o700); err != nil {
		return 0, err
	}
	position := 0
	err := repoworker.WithFileLock(filepath.Join(q.Root, ".queue.lock"), func() error {
		record, err := q.load(identity)
		if err != nil {
			return err
		}
		if record == nil {
			return q.save(identity, queueRecord{Schema: whale.GenerationSchema, LogicalRepoID: identity.LogicalRepoID, LogicalPath: identity.LogicalPath, Holder: identity.GenerationID, Waiting: []string{}})
		}
		if record.Holder == identity.GenerationID {
			return nil
		}
		for index, generationID := range record.Waiting {
			if generationID == identity.GenerationID {
				position = index + 1
				return nil
			}
		}
		record.Waiting = append(record.Waiting, identity.GenerationID)
		position = len(record.Waiting)
		return q.save(identity, *record)
	})
	return position, err
}

func (q PathQueue) Release(identity whale.Identity) error {
	if !filepath.IsAbs(q.Root) {
		return errors.New("Whale queue root must be absolute")
	}
	return repoworker.WithFileLock(filepath.Join(q.Root, ".queue.lock"), func() error {
		record, err := q.load(identity)
		if err != nil || record == nil {
			return err
		}
		if record.Holder != identity.GenerationID {
			for _, generationID := range record.Waiting {
				if generationID == identity.GenerationID {
					return errors.New("only the Whale path holder can release it")
				}
			}
			// The holder was already released and a later generation has been
			// promoted. Cleanup retry for the terminal generation is idempotent.
			return nil
		}
		if len(record.Waiting) == 0 {
			err := os.Remove(q.path(identity))
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		record.Holder = record.Waiting[0]
		record.Waiting = append([]string{}, record.Waiting[1:]...)
		return q.save(identity, *record)
	})
}

func (q PathQueue) load(identity whale.Identity) (*queueRecord, error) {
	raw, err := os.ReadFile(q.path(identity))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record queueRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	if record.Schema != whale.GenerationSchema || record.LogicalRepoID != identity.LogicalRepoID || record.LogicalPath != identity.LogicalPath || record.Holder == "" {
		return nil, errors.New("invalid Whale path queue")
	}
	return &record, nil
}

func (q PathQueue) save(identity whale.Identity, record queueRecord) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(q.Root, ".queue-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err == nil {
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
	if err := os.Rename(tmpPath, q.path(identity)); err != nil {
		return err
	}
	return syncDir(q.Root)
}

func (q PathQueue) path(identity whale.Identity) string {
	hash := sha256.Sum256([]byte(identity.LogicalRepoID + "\x00" + identity.LogicalPath))
	return filepath.Join(q.Root, hex.EncodeToString(hash[:])+".json")
}
