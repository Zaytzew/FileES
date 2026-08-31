package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// CanonicalState and YoungestRevision are populated for a durable record
	// which reached authority publication but never became active.  A
	// published+initializing repository is prunable only at exactly r0.
	CanonicalState   string `json:"canonical_state,omitempty"`
	YoungestRevision *int64 `json:"youngest_revision,omitempty"`
	// Prunable is either bookkeeping-only allocated/rolled_back state without
	// FSFS, or a published+initializing (and never activated) repository whose
	// FSFS is absent or exactly r0. prune_pending is the durable recovery marker
	// for the latter cleanup and may coexist with its own deleted tombstone.
	Prunable bool `json:"prunable"`
	path     string
}

// scanBackendRecords lists incomplete repository creation records under
// resultsRoot/backend, optionally filtered to one realm. A normal published
// record with active canonical authority and an FSFS tree is omitted. A
// published record is reported only when authority or storage is incomplete;
// published+initializing becomes prunable solely when the FSFS HEAD is r0.
func scanBackendRecords(ctx context.Context, resultsRoot, repositoriesRoot, serviceWC, svnLook, realmFilter string, now time.Time) ([]backendRecordReport, error) {
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
		if record.OperationID != operationID {
			continue
		}
		if realmFilter != "" && record.RealmID != realmFilter {
			continue
		}
		if _, err := uuid.Parse(record.RepoID); err != nil {
			return nil, fmt.Errorf("backend record %s has invalid repo_id", entry.Name())
		}
		fsfsPresent, finalPresent, stagingPresent := false, false, false
		if repositoriesRoot != "" {
			finalPresent, err = pathExists(filepath.Join(repositoriesRoot, record.RepoID))
			if err != nil {
				return nil, err
			}
			stagingPresent, err = pathExists(filepath.Join(repositoriesRoot, ".creating-"+operationID))
			if err != nil {
				return nil, err
			}
			fsfsPresent = finalPresent || stagingPresent
		}
		canonicalState := ""
		var youngest *int64
		prunable := !fsfsPresent && (record.Stage == "allocated" || record.Stage == "rolled_back")
		if record.Stage == "published" || record.Stage == "prune_pending" {
			canonicalState, err = readCanonicalRepositoryState(serviceWC, record.RepoID, record.RealmID)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			// A fully active published repository with its FSFS tree present is
			// the normal case and remains outside this cleanup report entirely.
			if record.Stage == "published" && canonicalState == "active" && finalPresent && !stagingPresent {
				continue
			}
			inspectEmpty := record.Stage == "published" && canonicalState == "initializing" ||
				record.Stage == "prune_pending" && (canonicalState == "initializing" || canonicalState == "deleted")
			if finalPresent && !stagingPresent && inspectEmpty {
				revision, youngestErr := readYoungestRevision(ctx, svnLook, filepath.Join(repositoriesRoot, record.RepoID))
				if youngestErr != nil {
					return nil, fmt.Errorf("inspect repository %s: %w", record.RepoID, youngestErr)
				}
				youngest = &revision
			}
			allowedState := record.Stage == "published" && canonicalState == "initializing" ||
				record.Stage == "prune_pending" && (canonicalState == "initializing" || canonicalState == "deleted")
			prunable = allowedState && !stagingPresent && (!finalPresent || (youngest != nil && *youngest == 0))
		}
		out = append(out, backendRecordReport{
			OperationID: record.OperationID, RealmID: record.RealmID, RepoID: record.RepoID,
			Name: record.Name, Stage: record.Stage, Purpose: record.Purpose,
			AgeSeconds: int64(now.Sub(info.ModTime()).Seconds()), FSFSPresent: fsfsPresent,
			CanonicalState: canonicalState, YoungestRevision: youngest, Prunable: prunable,
			path: fullPath,
		})
	}
	return out, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func readCanonicalRepositoryState(serviceWC, repoID, realmID string) (string, error) {
	if serviceWC == "" {
		return "", os.ErrNotExist
	}
	path := filepath.Join(serviceWC, "admin", "repositories", repoID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var record struct {
		Schema       string `json:"schema"`
		RepoID       string `json:"repo_id"`
		OwnerRealmID string `json:"owner_realm_id"`
		State        string `json:"state"`
	}
	if json.Unmarshal(raw, &record) != nil || record.Schema != "filees.repository/v1" || record.RepoID != repoID || record.OwnerRealmID != realmID {
		return "", errors.New("canonical repository record conflicts with backend")
	}
	return record.State, nil
}

func readYoungestRevision(ctx context.Context, svnLook, repository string) (int64, error) {
	if !filepath.IsAbs(svnLook) {
		return 0, errors.New("svnlook path must be absolute")
	}
	raw, err := exec.CommandContext(ctx, svnLook, "youngest", repository).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("svnlook youngest: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || revision < 0 {
		return 0, errors.New("svnlook returned invalid youngest revision")
	}
	return revision, nil
}
