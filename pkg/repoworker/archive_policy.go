package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

const RepositoryArchivePolicySchema = "filees.repository-archive-policy/v1"

// RepositoryArchivePolicy is canonical server authority, not an optional
// observation/cache or a client-writable SVN property. Freezing means the final
// guards are installed but existing locks may still need ordinary release.
// Administrative release is deliberately NOT represented by an empty policy.
type RepositoryArchivePolicy struct {
	Schema         string               `json:"schema"`
	OperationID    string               `json:"operation_id"`
	RepositoryUUID string               `json:"repository_uuid"`
	Revision       int64                `json:"revision"`
	Epoch          uint64               `json:"epoch"`
	State          string               `json:"state"` // preparing, freezing, archived, releasing, released
	StartedAt      time.Time            `json:"started_at"`
	Release        *ArchiveReleaseAudit `json:"release,omitempty"`
}

func (p RepositoryArchivePolicy) validate() error {
	if p.Schema != RepositoryArchivePolicySchema || p.Revision < 0 || p.Epoch == 0 || p.StartedAt.IsZero() ||
		(p.State != "preparing" && p.State != "freezing" && p.State != "archived" && p.State != "releasing" && p.State != "released") {
		return errors.New("canonical archive policy is invalid")
	}
	for _, id := range []string{p.OperationID, p.RepositoryUUID} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return errors.New("canonical archive policy contains invalid UUID")
		}
	}
	if p.State == "releasing" || p.State == "released" {
		if p.Release == nil {
			return errors.New("administrative release requires canonical audit")
		}
		return p.Release.validatePolicy(p)
	}
	if p.Release != nil {
		return errors.New("unexpected archive release audit")
	}
	return nil
}

func validateRepositoryArchive(record repositoryRecord, serviceWC string) error {
	if record.ArchivePolicy != nil {
		if err := record.ArchivePolicy.validate(); err != nil {
			return err
		}
		if record.ArchivePolicy.Release != nil {
			return verifyCanonicalArchiveRelease(serviceWC, record.RepoID, *record.ArchivePolicy.Release)
		}
	}
	return nil
}

func (r repositoryRecord) archiveProtected() bool {
	return r.ArchivePolicy != nil && r.ArchivePolicy.State != "released"
}

func (r repositoryRecord) archiveReadOnly() bool {
	// freezing has verified guards and retains raw SVN unlock. Early server
	// mutation admission still refuses every non-released protected state.
	return r.archiveProtected() && r.ArchivePolicy.State != "freezing"
}

// CheckCanonicalRepositoryWrite is an early admission check, independent of a
// stale rw projection. Call under the operation's existing writer/WC lease;
// final hooks remain necessary against an already admitted raw SVN client.
func CheckCanonicalRepositoryWrite(serviceWC, repoID string) error {
	if !filepath.IsAbs(serviceWC) {
		return errors.New("canonical archive authority requires an absolute working copy")
	}
	path, err := repositoryRecordPath(serviceWC, repoID)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var record repositoryRecord
	if json.Unmarshal(raw, &record) != nil || record.Schema != RepositorySchema || record.RepoID != repoID {
		return errors.New("canonical repository authority is invalid")
	}
	if err := validateRepositoryArchive(record, serviceWC); err != nil {
		return err
	}
	if record.archiveProtected() {
		return fmt.Errorf("%w (policy=%s)", ErrArchiveReadOnly, record.ArchivePolicy.State)
	}
	return nil
}

func (p ServicePublisher) publishArchivePolicy(ctx context.Context, record repositoryRecord, next RepositoryArchivePolicy) error {
	if err := p.validateGrantPublisher(); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return err
	}
	path, err := repositoryRecordPath(p.ServiceWC, record.RepoID)
	if err != nil {
		return err
	}
	var auditPath string
	if next.Release != nil {
		auditPath, err = writeCanonicalArchiveRelease(p.ServiceWC, record.RepoID, *next.Release)
		if err != nil {
			return err
		}
	}
	record.ArchivePolicy = &next
	if err := atomicJSON(path, record); err != nil {
		return err
	}
	changed, err := p.rebuildGrantAuthority()
	if err != nil {
		return err
	}
	// Include canonical authority even on retry: its previous Publish may have
	// failed after the local atomic write but before the service commit.
	paths := []string{path}
	if auditPath != "" {
		paths = append(paths, auditPath)
	}
	return p.Runner.Publish(ctx, append(paths, changed...), "filees: archive policy "+next.State+" for "+record.RepoID)
}
