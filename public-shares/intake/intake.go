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
	Now      func() time.Time
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
	if !filepath.IsAbs(s.Root) || s.MaxBytes < 1 || body == nil {
		return Record{}, ErrIncomplete
	}
	if _, err := uuid.Parse(channelID); err != nil || strings.TrimSpace(alias) == "" || strings.TrimSpace(slug) == "" || len(tokenSHA256) != sha256.Size*2 {
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Record{}, err
	}
	tmpPayload := filepath.Join(dir, "."+payloadName+".tmp")
	file, err := os.OpenFile(tmpPayload, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Record{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, s.MaxBytes+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.RemoveAll(dir)
		return Record{}, copyErr
	}
	if syncErr != nil {
		_ = os.RemoveAll(dir)
		return Record{}, syncErr
	}
	if closeErr != nil {
		_ = os.RemoveAll(dir)
		return Record{}, closeErr
	}
	if written == 0 {
		_ = os.RemoveAll(dir)
		return Record{}, ErrEmpty
	}
	if written > s.MaxBytes {
		_ = os.RemoveAll(dir)
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
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	if err := os.WriteFile(tmpMeta, append(raw, '\n'), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	metaFile, err := os.OpenFile(tmpMeta, os.O_RDWR, 0600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	if err := metaFile.Sync(); err != nil {
		metaFile.Close()
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	if err := metaFile.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	if err := os.Rename(tmpPayload, filepath.Join(dir, payloadName)); err != nil {
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	if err := os.Rename(tmpMeta, filepath.Join(dir, metaName)); err != nil {
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	ready := filepath.Join(dir, readyName)
	if err := os.WriteFile(ready, []byte(record.UploadID+"\n"), 0600); err != nil {
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	if err := durable.SyncDirectory(dir); err != nil {
		_ = os.RemoveAll(dir)
		return Record{}, err
	}
	if err := durable.SyncDirectory(filepath.Clean(s.Root)); err != nil {
		return Record{}, err
	}
	return record, nil
}

func boundedOriginalName(value string) (string, error) {
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
	return os.Rename(filepath.Join(dir, readyName), filepath.Join(dir, "PROCESSING"))
}

func (s Store) Release(uploadID string) error {
	if _, err := uuid.Parse(uploadID); err != nil || !filepath.IsAbs(s.Root) {
		return ErrIncomplete
	}
	dir := filepath.Join(filepath.Clean(s.Root), uploadID)
	return os.Rename(filepath.Join(dir, "PROCESSING"), filepath.Join(dir, readyName))
}

func (s Store) PayloadPath(uploadID string) string {
	return filepath.Join(filepath.Clean(s.Root), uploadID, payloadName)
}

func (s Store) Remove(uploadID string) error {
	if _, err := uuid.Parse(uploadID); err != nil || !filepath.IsAbs(s.Root) {
		return ErrIncomplete
	}
	return os.RemoveAll(filepath.Join(filepath.Clean(s.Root), uploadID))
}
