package uploadworker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	WaitingRoomTTL = 48 * time.Hour
	indexName      = "index.json"
	payloadName    = "payload"
	purgedLogName  = "purged.jsonl"
)

type Index struct {
	UploadID       string     `json:"upload_id"`
	OwnerRealm     string     `json:"owner_realm,omitempty"`
	OriginalName   string     `json:"original_name"`
	Size           int64      `json:"size"`
	SHA256         string     `json:"sha256,omitempty"`
	AVVerdict      string     `json:"av_verdict,omitempty"`
	RecipientToken string     `json:"recipient_token,omitempty"`
	ReceivedAt     time.Time  `json:"received_at"`
	Hidden         bool       `json:"hidden,omitempty"`
	PurgedAt       *time.Time `json:"purged_at,omitempty"`
	RelPath        string     `json:"-"`
}

func (idx Index) Remaining(now time.Time) time.Duration {
	if idx.ReceivedAt.IsZero() {
		return 0
	}
	left := idx.ReceivedAt.Add(WaitingRoomTTL).Sub(now)
	if left < 0 {
		return 0
	}
	return left
}

func (idx Index) Visible(now time.Time) bool {
	return !idx.Hidden && idx.PurgedAt == nil && idx.Remaining(now) > 0
}

func loadIndex(root, path string) (Index, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Index{}, err
	}
	var idx Index
	if json.Unmarshal(raw, &idx) != nil || idx.UploadID == "" || idx.OriginalName == "" || idx.ReceivedAt.IsZero() {
		return Index{}, errors.New("quarantine index is invalid")
	}
	if rel, err := filepath.Rel(root, filepath.Dir(path)); err == nil {
		idx.RelPath = filepath.ToSlash(rel)
	}
	return idx, nil
}

func writeIndex(path string, idx Index) error {
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}

func walkLiveIndexes(root string, now time.Time) ([]Index, error) {
	var out []Index
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Name() != indexName {
			return nil
		}
		idx, err := loadIndex(root, path)
		if err != nil {
			return nil
		}
		if idx.Visible(now) {
			out = append(out, idx)
		}
		return nil
	})
	return out, err
}

func remainingHours(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	hours := int((d + time.Hour - 1) / time.Hour)
	if hours < 1 {
		return 1
	}
	return hours
}

func sanitizeRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	return rel != "" && !strings.Contains(rel, "..") && !strings.HasPrefix(rel, "/")
}
