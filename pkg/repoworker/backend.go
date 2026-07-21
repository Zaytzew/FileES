package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"sync"
)

type Effects interface {
	CreateFSFS(context.Context, string, string) error
	PublishAuthority(context.Context, string, string, string, string) error
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
