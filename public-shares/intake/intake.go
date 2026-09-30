// Package intake is the public-host quarantine for Upload Channel.
//
// Bytes land under a random upload_id. The contributor filename is metadata
// only. Nothing here is listed, fetched, or committed to SVN.
package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"filees/internal/durable"
	"github.com/google/uuid"
)

const (
	Schema       = "filees.upload-intake/v1"
	StateReady   = "ready"
	payloadName  = "payload"
	metaName     = "meta.json"
	readyName    = "READY"
	maxNameBytes = 512
	// Job directories are created by _filees-links and consumed by
	// _filees-state. Both users are in _filees-public, so 0770/0660 is the
	// shared-topology mode; 0700/0600 left the reaper and clamscan blind.
	jobDirPerm  os.FileMode = 0770
	jobFilePerm os.FileMode = 0660
)

var (
	ErrIncomplete = errors.New("upload intake store is incomplete")
	ErrEmpty      = errors.New("upload payload is empty")
	ErrTooLarge   = errors.New("upload payload exceeds intake limit")
	ErrName       = errors.New("original filename is invalid")
)

type Store struct {
	Root     string
	MaxBytes int64
	// Zero means unlimited, as before (see budget.go).
	MaxUploadsPerChannel int
	MaxQuarantineBytes   int64
	Now                  func() time.Time
}

type Record struct {
	Schema       string    `json:"schema"`
	UploadID     string    `json:"upload_id"`
	ChannelID    string    `json:"channel_id"`
	Alias        string    `json:"alias"`
	Slug         string    `json:"slug"`
	OriginalName string    `json:"original_name"`
	Size         int64     `json:"size"`
	SHA256       string    `json:"sha256"`
	TokenSHA256  string    `json:"token_sha256"`
	ReceivedAt   time.Time `json:"received_at"`
	State        string    `json:"state"`
}

func (s Store) Accept(channelID, alias, slug, tokenSHA256, originalName string, body io.Reader) (Record, error) {
	if !filepath.IsAbs(s.Root) || s.MaxBytes < 1 || s.MaxBytes > 1<<40 || s.MaxUploadsPerChannel < 0 || s.MaxUploadsPerChannel > 1000000 || s.MaxQuarantineBytes < 0 || body == nil {
		return Record{}, ErrIncomplete
	}
	if !canonicalID(channelID) || strings.TrimSpace(alias) == "" || strings.TrimSpace(slug) == "" || len(tokenSHA256) != sha256.Size*2 {
		return Record{}, ErrIncomplete
	}
	if _, err := hex.DecodeString(tokenSHA256); err != nil {
		return Record{}, ErrIncomplete
	}
	name, err := boundedOriginalName(originalName)
	if err != nil {
		return Record{}, err
	}
	uploadID := uuid.NewString()
	dir := filepath.Join(filepath.Clean(s.Root), uploadID)
	if err := os.MkdirAll(s.Root, jobDirPerm); err != nil {
		return Record{}, err
	}
	if s.limited() {
		if err := s.reserve(channelID, uploadID); err != nil {
			return Record{}, err
		}
	}
	if err := os.MkdirAll(dir, jobDirPerm); err != nil {
		return Record{}, err
	}
	if err := os.Chmod(dir, jobDirPerm); err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	tmpPayload := filepath.Join(dir, "."+payloadName+".tmp")
	file, err := os.OpenFile(tmpPayload, os.O_CREATE|os.O_EXCL|os.O_WRONLY, jobFilePerm)
	if err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, s.MaxBytes))
	if copyErr == nil && written == s.MaxBytes {
		var extra [1]byte
		n, err := io.ReadFull(body, extra[:])
		if n > 0 {
			copyErr = ErrTooLarge
		} else if err != nil && err != io.EOF {
			copyErr = err
		}
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		_ = s.Remove(uploadID)
		return Record{}, copyErr
	}
	if syncErr != nil {
		_ = s.Remove(uploadID)
		return Record{}, syncErr
	}
	if closeErr != nil {
		_ = s.Remove(uploadID)
		return Record{}, closeErr
	}
	if written == 0 {
		_ = s.Remove(uploadID)
		return Record{}, ErrEmpty
	}
	if written > s.MaxBytes {
		_ = s.Remove(uploadID)
		return Record{}, ErrTooLarge
	}
	record := Record{
		Schema: Schema, UploadID: uploadID, ChannelID: channelID, Alias: alias, Slug: slug,
		OriginalName: name, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil)),
		TokenSHA256: tokenSHA256, ReceivedAt: s.now(), State: StateReady,
	}
	tmpMeta := filepath.Join(dir, "."+metaName+".tmp")
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	if err := os.WriteFile(tmpMeta, append(raw, '\n'), jobFilePerm); err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	metaFile, err := os.OpenFile(tmpMeta, os.O_RDWR, jobFilePerm)
	if err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	if err := metaFile.Sync(); err != nil {
		metaFile.Close()
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	if err := metaFile.Close(); err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	payloadPath := filepath.Join(dir, payloadName)
	if err := os.Rename(tmpPayload, payloadPath); err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	if err := os.Chmod(payloadPath, jobFilePerm); err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	metaPath := filepath.Join(dir, metaName)
	if err := os.Rename(tmpMeta, metaPath); err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	if err := os.Chmod(metaPath, jobFilePerm); err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	publish := func() error {
		ready := filepath.Join(dir, readyName)
		if err := os.WriteFile(ready, []byte(record.UploadID+"\n"), jobFilePerm); err != nil {
			return err
		}
		if err := os.Chmod(ready, jobFilePerm); err != nil {
			return err
		}
		// READY now carries the real size; the worst-case reservation goes.
		if err := os.Remove(filepath.Join(dir, reservationName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := durable.SyncDirectory(dir); err != nil {
			return err
		}
		return durable.SyncDirectory(filepath.Clean(s.Root))
	}
	if s.limited() {
		err = withBudgetLock(filepath.Join(s.Root, budgetLockName), func() error {
			if err := publish(); err != nil {
				// Do not let the reaper claim READY from a failed publication.
				_ = os.RemoveAll(dir)
				return err
			}
			return nil
		})
	} else {
		err = publish()
	}
	if err != nil {
		_ = s.Remove(uploadID)
		return Record{}, err
	}
	return record, nil
}

func boundedOriginalName(value string) (string, error) {
	// Check the received name before trimming: otherwise edge controls vanish
	// before validation. Bidi controls can disguise an extension in the UI;
	// ordinary right-to-left text and joiners remain valid Unicode filenames.
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return "", ErrName
		}
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxNameBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", ErrName
	}
	if strings.ContainsAny(value, `/\`) || strings.Contains(value, "..") {
		return "", ErrName
	}
	return value, nil
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Store) ListReady() ([]Record, error) {
	if !filepath.IsAbs(s.Root) {
		return nil, ErrIncomplete
	}
	entries, err := os.ReadDir(filepath.Clean(s.Root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.Root, entry.Name(), readyName)); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.Root, entry.Name(), metaName))
		if err != nil {
			return nil, err
		}
		var record Record
		if json.Unmarshal(raw, &record) != nil || record.UploadID != entry.Name() || record.State != StateReady {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func (s Store) Claim(uploadID string) error {
	if _, err := uuid.Parse(uploadID); err != nil || !filepath.IsAbs(s.Root) {
		return ErrIncomplete
	}
	dir := filepath.Join(filepath.Clean(s.Root), uploadID)
	return withBudgetLock(filepath.Join(s.Root, budgetLockName), func() error {
		return os.Rename(filepath.Join(dir, readyName), filepath.Join(dir, "PROCESSING"))
	})
}

func (s Store) Release(uploadID string) error {
	if _, err := uuid.Parse(uploadID); err != nil || !filepath.IsAbs(s.Root) {
		return ErrIncomplete
	}
	dir := filepath.Join(filepath.Clean(s.Root), uploadID)
	return withBudgetLock(filepath.Join(s.Root, budgetLockName), func() error {
		return os.Rename(filepath.Join(dir, "PROCESSING"), filepath.Join(dir, readyName))
	})
}

func (s Store) PayloadPath(uploadID string) string {
	return filepath.Join(filepath.Clean(s.Root), uploadID, payloadName)
}

func (s Store) Remove(uploadID string) error {
	if _, err := uuid.Parse(uploadID); err != nil || !filepath.IsAbs(s.Root) {
		return ErrIncomplete
	}
	if _, err := os.Stat(s.Root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return withBudgetLock(filepath.Join(s.Root, budgetLockName), func() error {
		return os.RemoveAll(filepath.Join(filepath.Clean(s.Root), uploadID))
	})
}
