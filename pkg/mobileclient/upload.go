package mobileclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	v1 "filees/pkg/mobile/v1"

	"github.com/google/uuid"
)

// UploadState is the local lifecycle of one queued append-only-unique
// candidate (concept doc FILEES_ANDROID_CLIENT_CONCEPT_V2.md §9.1). There is
// no "checked-out"/edit state: a retrieved object that the user wants to keep
// after editing elsewhere re-enters as a brand new PendingUpload (§7), never
// an update to an existing path.
type UploadState string

const (
	// UploadPendingCreate is queued, not yet sent or awaiting a retry after a
	// transport error. Durable and never silently overwritten or dropped by
	// manifest reconciliation (§9.3) regardless of what Refresh sees.
	UploadPendingCreate UploadState = "pending-create"
	// UploadUploading is in flight for the current drain attempt.
	UploadUploading UploadState = "uploading"
	// UploadCommitted is terminal: the worker published it as a new object.
	UploadCommitted UploadState = "committed"
	// UploadDroppedSame is terminal: NAME_TAKEN_SAME, an explicit dedup drop —
	// the user's intent ("this file is in the repo") is already satisfied.
	UploadDroppedSame UploadState = "dropped-duplicate"
	// UploadConflict is NAME_TAKEN_DIFF: parked for an explicit user decision
	// (new name, or discard). Never auto-resolved, never auto-renamed (§6.4).
	UploadConflict UploadState = "conflict"
	// UploadParked covers the remaining non-committed outcomes (destination
	// gone, access revoked, repo inactive, policy rejected) — parked rather
	// than silently dropped, so the caller can decide (§10.2).
	UploadParked UploadState = "parked"
)

// terminal reports whether state requires an explicit user/caller decision
// (or is already done) rather than being eligible for automatic drain.
func (s UploadState) terminal() bool {
	switch s {
	case UploadCommitted, UploadDroppedSame, UploadConflict, UploadParked:
		return true
	}
	return false
}

// PendingUpload is one queued append-only-unique candidate. ID doubles as the
// wire request_id: FileES reuses the same request_id across every drain
// attempt of the same candidate, which is what lets the worker ledger dedupe
// retries (concept doc §10.1) — a fresh id is only minted by EnqueueUpload for
// a genuinely new candidate.
type PendingUpload struct {
	ID          string      `json:"id"`
	RepoID      string      `json:"repo_id"`
	ParentPath  string      `json:"parent_path"`
	Filename    string      `json:"filename"`
	Size        int64       `json:"size"`
	Sha256      string      `json:"sha256"`
	ContentType string      `json:"content_type,omitempty"`
	State       UploadState `json:"state"`
	EnqueuedAt  time.Time   `json:"enqueued_at"`

	// Populated once a worker outcome is known.
	Outcome        v1.Outcome `json:"outcome,omitempty"`
	Revision       int64      `json:"revision,omitempty"`
	FinalPath      string     `json:"final_path,omitempty"`
	ExistingSha256 string     `json:"existing_sha256,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
}

func (s Store) uploadDir(repoID string) string {
	return filepath.Join(s.Root, "uploads", repoID)
}

func (s Store) uploadMetaPath(repoID, id string) string {
	return filepath.Join(s.uploadDir(repoID), id+".json")
}

func (s Store) uploadPayloadPath(repoID, id string) string {
	return filepath.Join(s.uploadDir(repoID), id+".bin")
}

// EnqueueUpload durably records a new pending-create candidate and its
// payload bytes under the non-evictable store root before any network
// attempt (§9.2: filesDir, never cacheDir — a lost pending upload is lost
// data). The payload is written and fsynced before the metadata that
// references it, so a crash never leaves metadata pointing at a missing file.
func (s Store) EnqueueUpload(repoID, parentPath, filename, contentType string, content []byte) (PendingUpload, error) {
	if strings.TrimSpace(repoID) == "" {
		return PendingUpload{}, errors.New("mobileclient: repo_id is required")
	}
	if strings.TrimSpace(filename) == "" {
		return PendingUpload{}, errors.New("mobileclient: filename is required")
	}
	sum := sha256.Sum256(content)
	item := PendingUpload{
		ID:          uuid.NewString(),
		RepoID:      repoID,
		ParentPath:  parentPath,
		Filename:    filename,
		Size:        int64(len(content)),
		Sha256:      hex.EncodeToString(sum[:]),
		ContentType: contentType,
		State:       UploadPendingCreate,
		EnqueuedAt:  time.Now().UTC(),
	}
	if err := atomicWriteBytes(s.uploadPayloadPath(repoID, item.ID), content); err != nil {
		return PendingUpload{}, fmt.Errorf("mobileclient: spool upload payload: %w", err)
	}
	if err := atomicWriteJSON(s.uploadMetaPath(repoID, item.ID), &item); err != nil {
		return PendingUpload{}, fmt.Errorf("mobileclient: persist upload metadata: %w", err)
	}
	return item, nil
}

// ListUploads returns every queued candidate for repoID, oldest first,
// regardless of state — including terminal/parked ones, so a caller (GUI)
// can show what still needs a decision.
func (s Store) ListUploads(repoID string) ([]PendingUpload, error) {
	entries, err := os.ReadDir(s.uploadDir(repoID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]PendingUpload, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		item, err := s.loadUploadMeta(repoID, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].EnqueuedAt.Before(items[j].EnqueuedAt) })
	return items, nil
}

func (s Store) loadUploadMeta(repoID, id string) (PendingUpload, error) {
	raw, err := os.ReadFile(s.uploadMetaPath(repoID, id))
	if err != nil {
		return PendingUpload{}, err
	}
	var item PendingUpload
	if err := json.Unmarshal(raw, &item); err != nil {
		return PendingUpload{}, fmt.Errorf("mobileclient: decode upload metadata %s: %w", id, err)
	}
	return item, nil
}

func (s Store) loadUploadPayload(repoID, id string) ([]byte, error) {
	raw, err := os.ReadFile(s.uploadPayloadPath(repoID, id))
	if err != nil {
		return nil, fmt.Errorf("mobileclient: read upload payload %s: %w", id, err)
	}
	return raw, nil
}

// recordUploadOutcome persists item's current state. Payload bytes are freed
// only once the candidate's intent is fulfilled — committed, or deduped as an
// identical existing object (§9.2: "usunięcie payloadu dozwolone dopiero po
// potwierdzonym receipcie"). Every other state, including conflict/parked,
// keeps the payload so a later decision (retry, rename-as-new-candidate) is
// still possible.
func (s Store) recordUploadOutcome(item PendingUpload) error {
	if err := atomicWriteJSON(s.uploadMetaPath(item.RepoID, item.ID), &item); err != nil {
		return fmt.Errorf("mobileclient: persist upload outcome: %w", err)
	}
	switch item.State {
	case UploadCommitted, UploadDroppedSame:
		if err := os.Remove(s.uploadPayloadPath(item.RepoID, item.ID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("mobileclient: free upload payload: %w", err)
		}
	}
	return nil
}

// DiscardUpload removes a queued candidate outright — the explicit "reject"
// half of the conflict/parked decision in §6.4 ("albo odrzuca"). It is a
// caller decision, never automatic.
func (s Store) DiscardUpload(repoID, id string) error {
	if err := os.Remove(s.uploadMetaPath(repoID, id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(s.uploadPayloadPath(repoID, id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
