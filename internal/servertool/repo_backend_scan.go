package servertool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// backendRecordReport mirrors the JSON shape of repoworker's unexported
// backendRecord (pkg/repoworker/backend.go) for read-only inspection from
// filees-admin. It deliberately does not import repoworker's internal type:
// this is a diagnostic/cleanup view over the same on-disk files, not a
// second writer of the durable create/publish state machine.
type backendRecordReport struct {
	OperationID string `json:"operation_id"`
	RealmID     string `json:"realm_id"`
	RepoID      string `json:"repo_id"`
	Name        string `json:"name"`
	Stage       string `json:"stage"`
	Purpose     string `json:"purpose,omitempty"`
	AgeSeconds  int64  `json:"age_seconds"`
	// FSFSPresent is true if a real SVN repository directory exists for this
	// record - either published (repositories.root/<repo_id>) or mid-creation
	// staging (repositories.root/.creating-<operation_id>, see
	// pkg/repoworker/effects.go ServerEffects.CreateFSFS). A record with real
	// FSFS content is never Prunable, regardless of Stage: removing only the
	// bookkeeping JSON would orphan a real repository directory on disk.
	FSFSPresent bool `json:"fsfs_present"`
	// Prunable is a bookkeeping-only record (Stage "allocated": nothing but
	// this JSON file ever existed, or "rolled_back": the FSFS side was
	// already cleanly removed by the normal rollback path) with no FSFS
	// content behind it. "fsfs_created" and "rollback_pending" are
	// deliberately never Prunable here - both can have a real, unpublished
	// FSFS repository or an interrupted rollback that needs the actual SVN
	// cleanup path (DurableBackend/Effects.RollbackCreate), not a bare file
	// delete. See concepts/ORPHANED_REPO_CLEANUP_CONCEPT.md §3.
	Prunable bool `json:"prunable"`
	path     string
}

// scanBackendRecords lists every non-"published" repository creation record
// under resultsRoot/backend, optionally filtered to one realm. "published"
// records (the normal, successful case) are never even reported - this is a
// tool for finding stuck/orphaned attempts, not a general repository lister.
func scanBackendRecords(resultsRoot, repositoriesRoot, realmFilter string, now time.Time) ([]backendRecordReport, error) {
	backendDir := filepath.Join(resultsRoot, "backend")
	entries, err := os.ReadDir(backendDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []backendRecordReport
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), "delete-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		operationID := strings.TrimSuffix(entry.Name(), ".json")
		if _, err := uuid.Parse(operationID); err != nil {
			continue
		}
		fullPath := filepath.Join(backendDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, err
		}
		var record struct {
			OperationID string `json:"operation_id"`
			RealmID     string `json:"realm_id"`
			Name        string `json:"name"`
			RepoID      string `json:"repo_id"`
			Stage       string `json:"stage"`
			Purpose     string `json:"purpose,omitempty"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, fmt.Errorf("decode backend record %s: %w", entry.Name(), err)
		}
		if record.OperationID != operationID || record.Stage == "published" {
			continue
		}
		if realmFilter != "" && record.RealmID != realmFilter {
			continue
		}
		fsfsPresent := false
		if repositoriesRoot != "" {
			if record.RepoID != "" {
				if _, statErr := os.Stat(filepath.Join(repositoriesRoot, record.RepoID)); statErr == nil {
					fsfsPresent = true
				}
			}
			if _, statErr := os.Stat(filepath.Join(repositoriesRoot, ".creating-"+operationID)); statErr == nil {
				fsfsPresent = true
			}
		}
		prunable := !fsfsPresent && (record.Stage == "allocated" || record.Stage == "rolled_back")
		out = append(out, backendRecordReport{
			OperationID: record.OperationID, RealmID: record.RealmID, RepoID: record.RepoID,
			Name: record.Name, Stage: record.Stage, Purpose: record.Purpose,
			AgeSeconds: int64(now.Sub(info.ModTime()).Seconds()), FSFSPresent: fsfsPresent, Prunable: prunable,
			path: fullPath,
		})
	}
	return out, nil
}
