package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Effects interface {
	CreateFSFS(context.Context, string, string) error
	PublishAuthority(context.Context, string, string, string, string) error
	PrepareDelete(context.Context, string, string) error
	WithdrawAuthority(context.Context, string, string) error
	ArchiveAndDeleteFSFS(context.Context, string, string) (time.Time, error)
}
type backendRecord struct {
	OperationID string `json:"operation_id"`
	RealmID     string `json:"realm_id"`
	Name        string `json:"name"`
	RepoID      string `json:"repo_id"`
	URL         string `json:"url"`
	Stage       string `json:"stage"`
}
type DurableBackend struct {
	Root, URLPrefix string
	Effects         Effects
	mu              sync.Mutex
}

type deleteBackendRecord struct {
	OperationID string `json:"operation_id"`
	RealmID     string `json:"realm_id"`
	RepoID      string `json:"repo_id"`
	Stage       string `json:"stage"`
	RetainUntil string `json:"retain_until,omitempty"`
}

func (b *DurableBackend) Create(ctx context.Context, op, realm, name string) (Repository, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !filepath.IsAbs(b.Root) || b.Effects == nil {
		return Repository{}, errors.New("repository backend is incomplete")
	}
	if _, e := uuid.Parse(op); e != nil {
		return Repository{}, e
	}
	if _, e := uuid.Parse(realm); e != nil {
		return Repository{}, e
	}
	if e := os.MkdirAll(b.Root, 0700); e != nil {
		return Repository{}, e
	}
	p := filepath.Join(b.Root, op+".json")
	r := backendRecord{}
	raw, e := os.ReadFile(p)
	if e == nil {
		if e = json.Unmarshal(raw, &r); e != nil {
			return Repository{}, e
		}
		if r.RealmID != realm || r.Name != name {
			return Repository{}, errors.New("operation parameters conflict")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return Repository{}, e
	} else {
		r = backendRecord{OperationID: op, RealmID: realm, Name: name, RepoID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(op)).String(), Stage: "allocated"}
		r.URL = b.URLPrefix + r.RepoID
		if e = b.save(p, r); e != nil {
			return Repository{}, e
		}
	}
	if r.Stage == "allocated" {
		if e = b.Effects.CreateFSFS(ctx, r.RepoID, r.OperationID); e != nil {
			return Repository{}, e
		}
		r.Stage = "fsfs_created"
		if e = b.save(p, r); e != nil {
			return Repository{}, e
		}
	}
	if r.Stage == "fsfs_created" {
		if e = b.Effects.PublishAuthority(ctx, r.RepoID, r.RealmID, r.Name, r.URL); e != nil {
			return Repository{}, e
		}
		r.Stage = "published"
		if e = b.save(p, r); e != nil {
			return Repository{}, e
		}
	}
	if r.Stage != "published" {
		return Repository{}, errors.New("invalid repository backend stage")
	}
	return Repository{RepoID: r.RepoID, URL: r.URL}, nil
}

func (b *DurableBackend) Delete(ctx context.Context, operationID, realmID, repoID string) (time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !filepath.IsAbs(b.Root) || b.Effects == nil {
		return time.Time{}, errors.New("repository backend is incomplete")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return time.Time{}, err
	}
	if _, err := uuid.Parse(realmID); err != nil {
		return time.Time{}, err
	}
	if _, err := uuid.Parse(repoID); err != nil {
		return time.Time{}, err
	}
	if err := os.MkdirAll(b.Root, 0700); err != nil {
		return time.Time{}, err
	}
	path := filepath.Join(b.Root, "delete-"+operationID+".json")
	record := deleteBackendRecord{}
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, &record); err != nil {
			return time.Time{}, err
		}
		if record.RealmID != realmID || record.RepoID != repoID {
			return time.Time{}, errors.New("repository deletion parameters conflict")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return time.Time{}, err
	} else {
		record = deleteBackendRecord{OperationID: operationID, RealmID: realmID, RepoID: repoID, Stage: "allocated"}
		if err := b.saveDelete(path, record); err != nil {
			return time.Time{}, err
		}
	}
	if record.Stage == "allocated" {
		if err := b.Effects.PrepareDelete(ctx, repoID, operationID); err != nil {
			return time.Time{}, err
		}
		record.Stage = "blocked"
		if err := b.saveDelete(path, record); err != nil {
			return time.Time{}, err
		}
	}
	if record.Stage == "blocked" {
		if err := b.Effects.WithdrawAuthority(ctx, repoID, realmID); err != nil {
			return time.Time{}, err
		}
		record.Stage = "withdrawn"
		if err := b.saveDelete(path, record); err != nil {
			return time.Time{}, err
		}
	}
	if record.Stage == "withdrawn" {
		retainUntil, err := b.Effects.ArchiveAndDeleteFSFS(ctx, repoID, operationID)
		if err != nil {
			return time.Time{}, err
		}
		record.Stage = "deleted"
		record.RetainUntil = retainUntil.UTC().Format(time.RFC3339Nano)
		if err := b.saveDelete(path, record); err != nil {
			return time.Time{}, err
		}
	}
	if record.Stage != "deleted" {
		return time.Time{}, errors.New("invalid repository deletion stage")
	}
	return time.Parse(time.RFC3339Nano, record.RetainUntil)
}

func (b *DurableBackend) saveDelete(path string, record deleteBackendRecord) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(b.Root, ".backend-delete-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(raw, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	directory, err := os.Open(b.Root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func (b *DurableBackend) save(path string, r backendRecord) error {
	raw, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(b.Root, ".backend-*.tmp")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(append(raw, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(b.Root)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
