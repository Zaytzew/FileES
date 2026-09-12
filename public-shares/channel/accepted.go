package channel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AcceptedSchema versions one arrival record on a shelf.
const AcceptedSchema = "filees.upload-accepted/v1"

// Accepted is one file that reached a shelf: what arrived, where it landed in
// the delivery repository, and when.
//
// It is deliberately **not** part of manifest.Upload. The manifest is the
// channel's declaration — who may contribute and what kind of shelf this is —
// and it changes through CreateUpload and UpdateUpload, which carry an
// operation ID and check the requester's realm. An arrival is state, not
// policy: writing it into the declaration would rewrite a policy document on
// every drop and race the owner's own updates. Quarantine already keeps its
// per-item index beside the manifest for the same reason.
//
// One file per arrival, never a list rewritten in place, so two accepts can
// never lose each other's record.
type Accepted struct {
	Schema    string `json:"schema"`
	ChannelID string `json:"channel_id"`
	UploadID  string `json:"upload_id"`

	// RepoPath is the name inside the delivery repository, after the naming
	// policy has had its say. It is what a selective fetch asks for, so it is
	// stored rather than recomputed from OriginalName.
	RepoPath     string `json:"repo_path"`
	OriginalName string `json:"original_name"`
	Size         int64  `json:"size,omitempty"`
	SHA256       string `json:"sha256,omitempty"`

	// Revision is the delivery repository revision that carries this file.
	// Zero when the commit output could not be read: the arrival is still
	// recorded, because losing the record would be worse than losing the
	// number, and a browser can live without it.
	Revision int64 `json:"revision,omitempty"`

	AcceptedAt time.Time `json:"accepted_at"`

	// Event is the encrypted record of which invitation was exercised, when
	// the file was received and from where. Opaque here on purpose: it is
	// resolvable only with the private key kept apart from this machine, and
	// raw parsing of this file must say nothing about a person
	// (UPLOAD_CHANNEL_CONCEPT.md §10a). Empty until that key pair exists.
	Event string `json:"event,omitempty"`
}

func (s *Store) acceptedDir(channelID string) string {
	return filepath.Join(s.Root, "upload-accepted", channelID)
}

func (s *Store) acceptedPath(channelID, uploadID string) string {
	return filepath.Join(s.acceptedDir(channelID), uploadID+".json")
}

// RecordAccepted stores one arrival. It is idempotent by upload ID: a reaper
// that crashed between the commit and this write may run the same job again,
// and a second record of the same arrival must not become a second entry.
func (s *Store) RecordAccepted(entry Accepted) error {
	if strings.TrimSpace(entry.ChannelID) == "" || strings.TrimSpace(entry.UploadID) == "" {
		return errors.New("accepted record needs a channel and an upload id")
	}
	if strings.ContainsAny(entry.ChannelID+entry.UploadID, `/\`) {
		return errors.New("accepted record identifiers must not contain separators")
	}
	entry.Schema = AcceptedSchema
	if entry.AcceptedAt.IsZero() {
		entry.AcceptedAt = s.now()
	}
	return atomicJSON(s.acceptedPath(entry.ChannelID, entry.UploadID), 0600, entry)
}

// ListAccepted returns what is on a shelf, oldest first, so a reader sees the
// order things arrived in. An absent directory is an empty shelf, not an
// error: a channel that has never received anything is an ordinary state.
func (s *Store) ListAccepted(channelID string) ([]Accepted, error) {
	entries, err := os.ReadDir(s.acceptedDir(channelID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Accepted, 0, len(entries))
	for _, file := range entries {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.acceptedDir(channelID), file.Name()))
		if err != nil {
			// One unreadable record must not hide the rest of the shelf.
			continue
		}
		var record Accepted
		if json.Unmarshal(raw, &record) != nil || record.Schema != AcceptedSchema || record.UploadID == "" {
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AcceptedAt.Equal(out[j].AcceptedAt) {
			return out[i].AcceptedAt.Before(out[j].AcceptedAt)
		}
		return out[i].UploadID < out[j].UploadID
	})
	return out, nil
}

// ForgetAccepted drops the records for a shelf that was cleared on the server.
// Clearing empties the shelf at HEAD; the repository keeps its history and so
// does the owner's ability to look at it. These records describe what is left
// to handle, so they go when nothing is left.
func (s *Store) ForgetAccepted(channelID string, uploadIDs []string) error {
	for _, uploadID := range uploadIDs {
		if strings.TrimSpace(uploadID) == "" || strings.ContainsAny(uploadID, `/\`) {
			continue
		}
		if err := os.Remove(s.acceptedPath(channelID, uploadID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
