package uploadworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/avscan"
	"github.com/google/uuid"
)

type PurgedItem struct {
	UploadID     string    `json:"upload_id"`
	OriginalName string    `json:"original_name"`
	PurgedAt     time.Time `json:"purged_at"`
}

type WaitingList struct {
	Entries []WaitingEntry
	Purged  []PurgedItem
}

type WaitingEntry struct {
	Index
	RemainingHours int
}

func (r Reaper) ListWaiting(ownerRealm string, now time.Time) (WaitingList, error) {
	if !filepath.IsAbs(r.TrashRoot) {
		return WaitingList{}, ErrIncomplete
	}
	if err := r.PurgeExpired(context.Background(), now); err != nil {
		return WaitingList{}, err
	}
	live, err := walkLiveIndexes(r.TrashRoot, now)
	if err != nil {
		return WaitingList{}, err
	}
	list := WaitingList{Entries: make([]WaitingEntry, 0, len(live))}
	for _, idx := range live {
		if ownerRealm != "" && idx.OwnerRealm != "" && idx.OwnerRealm != ownerRealm {
			continue
		}
		list.Entries = append(list.Entries, WaitingEntry{Index: idx, RemainingHours: remainingHours(idx.Remaining(now))})
	}
	purged, err := readPurgedLog(r.TrashRoot, now)
	if err != nil {
		return WaitingList{}, err
	}
	list.Purged = purged
	return list, nil
}

func (r Reaper) SeedReject(ownerRealm, originalName string, now time.Time) (Index, error) {
	if !filepath.IsAbs(r.TrashRoot) || ownerRealm == "" {
		return Index{}, ErrIncomplete
	}
	if now.IsZero() {
		now = r.now().UTC()
	} else {
		now = now.UTC()
	}
	name := filepath.Base(filepath.ToSlash(originalName))
	if name == "" || name == "." || name == ".." {
		name = "eicar.com"
	}
	payload := []byte(avscan.EICAR)
	sum := sha256.Sum256(payload)
	id := uuid.NewString()
	rel := "seed/" + now.Format("2006-01-02") + "/" + id
	dir := filepath.Join(r.TrashRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Index{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, payloadName), payload, 0600); err != nil {
		return Index{}, err
	}
	idx := Index{
		UploadID: id, OwnerRealm: ownerRealm, OriginalName: name, Size: int64(len(payload)),
		SHA256: hex.EncodeToString(sum[:]), AVVerdict: avscan.EICARSignature, ReceivedAt: now,
	}
	if err := writeIndex(filepath.Join(dir, indexName), idx); err != nil {
		return Index{}, err
	}
	idx.RelPath = rel
	return idx, nil
}

func (r Reaper) HideWaiting(uploadID string, now time.Time) error {
	idx, path, err := r.findIndex(uploadID)
	if err != nil {
		return err
	}
	if !idx.Visible(now) {
		return ErrNotFound
	}
	idx.Hidden = true
	return writeIndex(path, idx)
}

func (r Reaper) FetchWaiting(uploadID string, now time.Time) (Index, []byte, int, error) {
	idx, _, err := r.findIndex(uploadID)
	if err != nil {
		return Index{}, nil, 0, err
	}
	if !idx.Visible(now) || !sanitizeRel(idx.RelPath) {
		return Index{}, nil, 0, ErrNotFound
	}
	raw, err := os.ReadFile(filepath.Join(r.TrashRoot, filepath.FromSlash(idx.RelPath), payloadName))
	if err != nil {
		return Index{}, nil, 0, err
	}
	return idx, raw, remainingHours(idx.Remaining(now)), nil
}

func (r Reaper) PurgeExpired(ctx context.Context, now time.Time) error {
	if !filepath.IsAbs(r.TrashRoot) {
		return ErrIncomplete
	}
	return filepath.WalkDir(r.TrashRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Name() != indexName {
			return nil
		}
		idx, err := loadIndex(r.TrashRoot, path)
		if err != nil || idx.PurgedAt != nil || idx.Remaining(now) > 0 {
			return nil
		}
		purged := now.UTC()
		idx.PurgedAt = &purged
		_ = writeIndex(path, idx)
		_ = appendPurgedLog(r.TrashRoot, PurgedItem{UploadID: idx.UploadID, OriginalName: idx.OriginalName, PurgedAt: purged})
		dir := filepath.Dir(path)
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return nil
	})
}

func (r Reaper) findIndex(uploadID string) (Index, string, error) {
	var found Index
	var foundPath string
	err := filepath.WalkDir(r.TrashRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Name() != indexName {
			return walkErr
		}
		idx, err := loadIndex(r.TrashRoot, path)
		if err != nil || idx.UploadID != uploadID {
			return nil
		}
		found, foundPath = idx, path
		return filepath.SkipAll
	})
	if err != nil {
		return Index{}, "", err
	}
	if foundPath == "" {
		return Index{}, "", ErrNotFound
	}
	return found, foundPath, nil
}

func appendPurgedLog(root string, item PurgedItem) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, purgedLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

func readPurgedLog(root string, now time.Time) ([]PurgedItem, error) {
	raw, err := os.ReadFile(filepath.Join(root, purgedLogName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []PurgedItem
	for _, line := range splitLines(raw) {
		var item PurgedItem
		if json.Unmarshal(line, &item) != nil {
			continue
		}
		if now.Sub(item.PurgedAt) <= WaitingRoomTTL {
			out = append(out, item)
		}
	}
	return out, nil
}

func splitLines(raw []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range raw {
		if b != '\n' {
			continue
		}
		if i > start {
			lines = append(lines, raw[start:i])
		}
		start = i + 1
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}
