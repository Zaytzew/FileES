package localrepo

import (
	"encoding/hex"
	"errors"
	"strings"

	"filees/pkg/clientview"
	"github.com/google/uuid"
)

// ShelfFetch is one durable selected-file receipt, independent of WC state.
// A new selection cannot replace a running selection; completion survives restart.
type ShelfFetch struct {
	Placement ShelfPlacement `json:"placement,omitempty"`
	ID        string         `json:"id,omitempty"`
	UploadID  string         `json:"upload_id,omitempty"`
	RepoPath  string         `json:"repo_path,omitempty"`
	SHA256    string         `json:"sha256,omitempty"`
	Size      int64          `json:"size,omitempty"`
	State     string         `json:"state,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// ShelfPlacement is a local copy intent, never a server-side cross-repo move.
type ShelfPlacement struct {
	ParentRepoID string `json:"parent_repo_id,omitempty"`
	ParentRoot   string `json:"parent_root,omitempty"`
	ParentURL    string `json:"parent_url,omitempty"`
	RelativePath string `json:"relative_path,omitempty"`
}

func ValidShelfPath(path string) bool {
	if path == "" || strings.ContainsAny(path, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") || strings.TrimRight(part, " .") != part {
			return false
		}
	}
	return true
}

func (s *Store) QueueShelfFetch(operationID, uploadID, path, hash string, size int64, placement ...ShelfPlacement) (Record, error) {
	var target ShelfPlacement
	if len(placement) > 1 {
		return Record{}, errors.New("invalid placement")
	}
	if len(placement) == 1 {
		target = placement[0]
		if target.ParentRepoID == "" || target.ParentRoot == "" || target.ParentURL == "" || !ValidShelfPath(target.RelativePath) {
			return Record{}, errors.New("invalid shelf placement")
		}
	}
	decoded, err := hex.DecodeString(hash)
	if uploadID == "" || !ValidShelfPath(path) || err != nil || len(decoded) != 32 || size < 0 {
		return Record{}, errors.New("invalid shelf selection")
	}
	return s.update(operationID, func(r *Record) error {
		if r.Purpose != clientview.PurposeUploadShelf || r.Access != "r" || (r.State != StateAttached && r.State != StateAttaching) {
			return errors.New("shelf is not available")
		}
		if r.ShelfFetch.State == "queued" || r.ShelfFetch.State == "running" {
			return errors.New("shelf download is already pending")
		}
		r.ShelfFetch = ShelfFetch{ID: uuid.NewString(), UploadID: uploadID, RepoPath: path, SHA256: strings.ToLower(hash), Size: size, State: "queued"}
		r.ShelfFetch.Placement = target
		return nil
	})
}

func (s *Store) SetShelfFetchState(operationID, fetchID, state string, cause error) (Record, error) {
	return s.update(operationID, func(r *Record) error {
		if fetchID == "" || r.ShelfFetch.ID != fetchID || (r.ShelfFetch.State != "queued" && r.ShelfFetch.State != "running") {
			return errors.New("shelf download is no longer pending")
		}
		if state != "running" && state != "complete" && state != "failed" {
			return errors.New("invalid shelf download state")
		}
		r.ShelfFetch.State, r.ShelfFetch.Error = state, ""
		if cause != nil {
			r.ShelfFetch.Error = cause.Error()
		}
		return nil
	})
}
