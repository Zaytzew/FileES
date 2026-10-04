package channel

import (
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const uploadOutcomeSchema = "filees.upload-outcome/v1"
const uploadOutcomeLifetime = 7 * 24 * time.Hour

// UploadStatus contains no contributor identifier, server path or tool error.
type UploadStatus struct {
	UploadID string `json:"upload_id"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	RepoPath string `json:"repo_path,omitempty"`
	Size     int64  `json:"size,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Revision int64  `json:"revision,omitempty"`
}

type uploadOutcome struct {
	Schema      string    `json:"schema"`
	ChannelID   string    `json:"channel_id"`
	TokenSHA256 string    `json:"token_sha256"`
	UpdatedAt   time.Time `json:"updated_at"`
	UploadStatus
}

func validUploadID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

func validTokenHash(hash string) bool {
	raw, err := hex.DecodeString(hash)
	return err == nil && len(raw) == 32
}

// RecordUploadOutcome uses the existing private arrival-index subtree. A
// receipt survives clearing the owner's shelf and expires after seven days.
func (s *Store) RecordUploadOutcome(channelID, tokenHash string, status UploadStatus) error {
	if !validUploadID(channelID) || !validUploadID(status.UploadID) || !validTokenHash(tokenHash) || !status.Valid() || status.State == "pending" {
		return errors.New("invalid upload outcome")
	}
	return atomicJSON(filepath.Join(s.acceptedDir(channelID), status.UploadID+".outcome.json"), 0600,
		uploadOutcome{Schema: uploadOutcomeSchema, ChannelID: channelID, TokenSHA256: tokenHash, UpdatedAt: s.now(), UploadStatus: status})
}

func (status UploadStatus) Valid() bool {
	if !validUploadID(status.UploadID) {
		return false
	}
	if status.State == "accepted" {
		return status.Reason == "" && status.RepoPath != "" && len(status.RepoPath) <= 4096 && !strings.ContainsAny(status.RepoPath, "\\\x00\r\n") && status.Size > 0 && validTokenHash(status.SHA256) && status.Revision >= 0
	}
	if status.RepoPath != "" || status.Size != 0 || status.SHA256 != "" || status.Revision != 0 {
		return false
	}
	if status.State == "pending" {
		return status.Reason == ""
	}
	if status.State == "rejected" {
		return status.Reason == "antivirus"
	}
	if status.State != "attention" {
		return false
	}
	switch status.Reason {
	case "collision", "scanner", "storage", "confirmation", "operation":
		return true
	}
	return false
}

// ReadUploadStatus binds a receipt to its channel, upload ID and invitation.
// Missing proof remains pending, even if intake has already disappeared.
func (s *Store) ReadUploadStatus(channelID, uploadID, tokenHash string) (UploadStatus, error) {
	if !validUploadID(channelID) || !validUploadID(uploadID) || !validTokenHash(tokenHash) {
		return UploadStatus{}, ErrNotFound
	}
	var accepted Accepted
	err := readUploadReceipt(s.acceptedDir(channelID), uploadID+".json", &accepted)
	if err == nil {
		if accepted.Schema != AcceptedSchema || accepted.ChannelID != channelID || accepted.UploadID != uploadID ||
			!hmac.Equal([]byte(accepted.TokenSHA256), []byte(tokenHash)) {
			return UploadStatus{}, ErrNotFound
		}
		status := UploadStatus{UploadID: uploadID, State: "accepted", RepoPath: accepted.RepoPath, Size: accepted.Size, SHA256: accepted.SHA256, Revision: accepted.Revision}
		if !status.Valid() {
			return UploadStatus{}, ErrNotFound
		}
		return status, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return UploadStatus{}, err
	}
	var outcome uploadOutcome
	err = readUploadReceipt(s.acceptedDir(channelID), uploadID+".outcome.json", &outcome)
	if err == nil {
		if outcome.Schema != uploadOutcomeSchema || outcome.ChannelID != channelID || outcome.UploadID != uploadID ||
			!hmac.Equal([]byte(outcome.TokenSHA256), []byte(tokenHash)) || !outcome.UploadStatus.Valid() || outcome.UpdatedAt.IsZero() || outcome.UpdatedAt.Add(uploadOutcomeLifetime).Before(s.now()) {
			return UploadStatus{}, ErrNotFound
		}
		return outcome.UploadStatus, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return UploadStatus{}, err
	}
	return UploadStatus{UploadID: uploadID, State: "pending"}, nil
}

// PurgeUploadOutcomes removes only understood expired receipts, without
// following symlinks or touching shelf-arrival records, payloads or policy.
func (s *Store) PurgeUploadOutcomes() error {
	dirPath := filepath.Join(s.Root, "upload-accepted")
	info, err := os.Lstat(dirPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Optional receipt GC must not turn an index-write failure into a
	// pre-commit failure. The existing reaper reports committed-but-unindexed
	// files separately. Never follow an unexpected file or symlink here.
	if !info.IsDir() {
		return nil
	}
	root, err := os.OpenRoot(dirPath)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	channels, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, c := range channels {
		if !c.IsDir() || !validUploadID(c.Name()) {
			continue
		}
		sub, err := root.OpenRoot(c.Name())
		if err != nil {
			return err
		}
		err = purgeUploadOutcomes(sub, c.Name(), s.now())
		sub.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func purgeUploadOutcomes(root *os.Root, channelID string, now time.Time) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	files, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, file := range files {
		id := strings.TrimSuffix(file.Name(), ".outcome.json")
		if !strings.HasSuffix(file.Name(), ".outcome.json") || !validUploadID(id) {
			continue
		}
		info, err := root.Lstat(file.Name())
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 16<<10 {
			continue
		}
		raw, err := root.ReadFile(file.Name())
		if err != nil {
			return err
		}
		var outcome uploadOutcome
		if json.Unmarshal(raw, &outcome) != nil || outcome.Schema != uploadOutcomeSchema || outcome.ChannelID != channelID || outcome.UploadID != id || !validTokenHash(outcome.TokenSHA256) || !outcome.Valid() || outcome.UpdatedAt.IsZero() {
			continue
		}
		if !outcome.UpdatedAt.Add(uploadOutcomeLifetime).Before(now) {
			continue
		}
		if err := root.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func readUploadReceipt(dir, name string, out any) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<10 {
		return errors.New("invalid upload receipt")
	}
	raw, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	if len(raw) > 16<<10 {
		return errors.New("oversized upload receipt")
	}
	return json.Unmarshal(raw, out)
}
