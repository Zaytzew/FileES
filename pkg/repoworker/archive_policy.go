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
	Schema         string    `json:"schema"`
	OperationID    string    `json:"operation_id"`
	RepositoryUUID string    `json:"repository_uuid"`
	Revision       int64     `json:"revision"`
	Epoch          uint64    `json:"epoch"`
	State          string    `json:"state"` // preparing, freezing, archived
	StartedAt      time.Time `json:"started_at"`
}

func (p RepositoryArchivePolicy) validate() error {
	if p.Schema != RepositoryArchivePolicySchema || p.Revision < 0 || p.Epoch == 0 || p.StartedAt.IsZero() ||
		(p.State != "preparing" && p.State != "freezing" && p.State != "archived") {
		return errors.New("canonical archive policy is invalid")
	}
	for _, id := range []string{p.OperationID, p.RepositoryUUID} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return errors.New("canonical archive policy contains invalid UUID")
		}
	}
	return nil
}

func validateRepositoryArchive(record repositoryRecord) error {
	if record.ArchivePolicy != nil {
		return record.ArchivePolicy.validate()
	}
	return nil
}

func (r repositoryRecord) archiveReadOnly() bool {
	// freezing has verified guards and retains raw SVN unlock. Early server
	// mutation admission still refuses ALL three protected states.
	return r.ArchivePolicy != nil && r.ArchivePolicy.State != "freezing"
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
	if err := validateRepositoryArchive(record); err != nil {
		return err
	}
	if record.ArchivePolicy != nil {
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
	return p.Runner.Publish(ctx, append([]string{path}, changed...), "filees: archive policy "+next.State+" for "+record.RepoID)
}
