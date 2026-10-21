package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

const archiveAdmissionSchema = "filees.archive-admission/v1"

// ArchiveAdmissionFence is a short, server-wide raw-write stop during the
// initial guard transition, NOT during an import or an ordinary sync tick.
// Reads continue. The existing global worker lock serializes transitions;
// one unfinished binding cannot be replaced by a different operation.
type ArchiveAdmissionFence struct{ Root string } // existing activation root

type ArchiveAdmissionBinding struct {
	Schema         string    `json:"schema"`
	RepoID         string    `json:"repo_id"`
	OperationID    string    `json:"operation_id"`
	RepositoryUUID string    `json:"repository_uuid"`
	Revision       int64     `json:"revision"`
	Epoch          uint64    `json:"epoch"`
	State          string    `json:"state"`
	ChangedAt      time.Time `json:"changed_at"`
}

func (r ArchiveAdmissionBinding) validate() error {
	if r.Schema != archiveAdmissionSchema || r.Epoch == 0 || r.Revision < 0 || r.ChangedAt.IsZero() || (r.State != "held" && r.State != "open") {
		return errors.New("invalid archive admission state")
	}
	for _, id := range []string{r.RepoID, r.OperationID, r.RepositoryUUID} {
		if p, err := uuid.Parse(id); err != nil || p.String() != id {
			return errors.New("invalid archive admission identity")
		}
	}
	return nil
}

func (f ArchiveAdmissionFence) directory() (string, error) {
	if !filepath.IsAbs(f.Root) {
		return "", errors.New("archive admission root must be absolute")
	}
	root := filepath.Join(f.Root, "archive-admission")
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("archive admission must be a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return root, nil
}

func (f ArchiveAdmissionFence) read() (*ArchiveAdmissionBinding, error) {
	root, err := f.directory()
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "state.json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("unsafe archive admission file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var record ArchiveAdmissionBinding
	d := json.NewDecoder(io.LimitReader(file, 4097))
	d.DisallowUnknownFields()
	if err := d.Decode(&record); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing archive admission data")
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	return &record, nil
}

func (f ArchiveAdmissionFence) Held() (bool, error) {
	r, err := f.read()
	if err != nil {
		return true, err
	}
	return r != nil && r.State == "held", nil
}

func (f ArchiveAdmissionFence) Hold(ctx context.Context, repo string, plan RepositoryArchivePolicy) error {
	want := ArchiveAdmissionBinding{Schema: archiveAdmissionSchema, RepoID: repo, OperationID: plan.OperationID, RepositoryUUID: plan.RepositoryUUID, Revision: plan.Revision, Epoch: plan.Epoch, State: "held", ChangedAt: time.Now().UTC()}
	if err := want.validate(); err != nil {
		return err
	}
	root, err := f.directory()
	if err != nil {
		return err
	}
	return WithFileLockContext(ctx, filepath.Join(f.Root, ".archive-admission.lock"), func() error {
		existing, err := f.read()
		if err != nil {
			return err
		}
		if existing == nil {
			// Publish the sentinel AND its required state together. A process
			// exit cannot leave an empty directory requiring manual recovery.
			staging, err := os.MkdirTemp(f.Root, ".archive-admission-")
			if err != nil {
				return err
			}
			defer os.Remove(staging)
			defer os.Remove(filepath.Join(staging, "state.json"))
			if err := os.Chmod(staging, 0755); err != nil {
				return err
			}
			if err := writeArchiveAdmission(staging, want); err != nil {
				return err
			}
			if err := os.Rename(staging, root); err != nil {
				return err
			}
			return syncDirectory(f.Root)
		}
		if existing != nil && existing.State == "held" {
			if !existing.matches(repo, plan) {
				return errors.New("another archive transition is pending")
			}
			return nil
		}
		return writeArchiveAdmission(root, want)
	})
}

func (r ArchiveAdmissionBinding) matches(repo string, p RepositoryArchivePolicy) bool {
	return r.RepoID == repo && r.OperationID == p.OperationID && r.Epoch == p.Epoch && r.RepositoryUUID == p.RepositoryUUID && r.Revision == p.Revision
}

func (f ArchiveAdmissionFence) Open(ctx context.Context, repo string, plan RepositoryArchivePolicy) error {
	root, err := f.directory()
	if err != nil {
		return err
	}
	return WithFileLockContext(ctx, filepath.Join(f.Root, ".archive-admission.lock"), func() error {
		r, err := f.read()
		if err != nil {
			return err
		}
		if r == nil || !r.matches(repo, plan) {
			return errors.New("archive admission release binding changed")
		}
		if r.State == "open" {
			return nil
		}
		r.State, r.ChangedAt = "open", time.Now().UTC()
		return writeArchiveAdmission(root, *r)
	})
}

func writeArchiveAdmission(root string, r ArchiveAdmissionBinding) error {
	if err := r.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := archiveProtectionTemp(root, append(raw, '\n'))
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, filepath.Join(root, "state.json")); err != nil {
		return err
	}
	return syncDirectory(root)
}
