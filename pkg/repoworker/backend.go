package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Effects interface {
	CreateFSFS(context.Context, string, string) error
	PublishAuthority(context.Context, string, string, string, string) error
	RollbackCreate(context.Context, string, string) error
	// AuthorizeDelete is a side-effect-free ownership boundary. It must run
	// before PrepareDelete installs a commit-blocking hook in the FSFS tree.
	AuthorizeDelete(context.Context, string, string) error
	PrepareDelete(context.Context, string, string) error
	RestoreDelete(context.Context, string, string) error
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
		r.URL, e = repositoryURL(b.URLPrefix, r.RepoID)
		if e != nil {
			return Repository{}, e
		}
		if e = b.save(p, r); e != nil {
			return Repository{}, e
		}
	}
	if r.Stage == "rollback_pending" || r.Stage == "rolled_back" {
		if e = b.rollbackCreate(ctx, p, &r); e != nil {
			return Repository{}, fmt.Errorf("resume failed repository rollback: %w", e)
		}
		return Repository{}, errors.New("previous repository creation was rolled back; submit a new create request")
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
			publishErr := e
			r.Stage = "rollback_pending"
			if e = b.save(p, r); e != nil {
				return Repository{}, fmt.Errorf("record failed repository rollback: %w", e)
			}
			if e = b.rollbackCreate(ctx, p, &r); e != nil {
				return Repository{}, fmt.Errorf("publish authority: %w; rollback pending: %v", publishErr, e)
			}
			return Repository{}, fmt.Errorf("publish authority: %w; repository was rolled back", publishErr)
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

// ValidateURLPrefix rejects a repository URL prefix that could never appear
// in a client projection. Running this before svnadmin prevents a server
// configuration typo from creating an orphaned FSFS repository.
func ValidateURLPrefix(prefix string) error {
	_, err := repositoryURL(prefix, uuid.NewString())
	return err
}

func repositoryURL(prefix, repoID string) (string, error) {
	if strings.TrimSpace(prefix) == "" || strings.TrimSpace(prefix) != prefix || !strings.HasSuffix(prefix, "/") {
		return "", errors.New("repository URL prefix must be a non-empty restricted svn+ssh URL ending in /")
	}
	parsed, err := url.Parse(prefix + repoID)
	if err != nil || parsed.Scheme != "svn+ssh" || parsed.Hostname() == "" || parsed.User == nil || (parsed.User.Username() != "_filees-client" && parsed.User.Username() != "_filees-data") || parsed.User.String() != parsed.User.Username() || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("repository URL prefix must use restricted svn+ssh transport")
	}
	return prefix + repoID, nil
}

// rollbackCreate finishes the compensation recorded before any server-side
// state is removed. Keeping rollback_pending durable makes an interrupted
// rollback retryable on the same operation without ever retrying publication.
func (b *DurableBackend) rollbackCreate(ctx context.Context, path string, record *backendRecord) error {
	if record.Stage == "rollback_pending" {
		if err := b.Effects.RollbackCreate(ctx, record.RepoID, record.RealmID); err != nil {
			return err
		}
		record.Stage = "rolled_back"
		if err := b.save(path, *record); err != nil {
			return err
		}
	}
	if record.Stage != "rolled_back" {
		return errors.New("invalid repository rollback stage")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory, err := os.Open(b.Root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// ReapFailedCreates completes only creation rollbacks which were durably
// marked before compensation began. It never guesses about fsfs_created: that
// stage can still be a recoverable in-flight publication from an older worker.
func (b *DurableBackend) ReapFailedCreates(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !filepath.IsAbs(b.Root) || b.Effects == nil {
		return errors.New("repository backend is incomplete")
	}
	entries, err := os.ReadDir(b.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), "delete-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		operationID := strings.TrimSuffix(entry.Name(), ".json")
		if _, err := uuid.Parse(operationID); err != nil {
			continue
		}
		path := filepath.Join(b.Root, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var record backendRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.OperationID != operationID || (record.Stage != "rollback_pending" && record.Stage != "rolled_back") {
			continue
		}
		if err := b.rollbackCreate(ctx, path, &record); err != nil {
			return fmt.Errorf("rollback failed create %s: %w", operationID, err)
		}
	}
	return nil
}

// ReapUncommittedDeletes compensates only delete preparations which never
// crossed the durable authority boundary. Records remain allocated so a retry
// with the same operation ID keeps its parameter binding and can prepare the
// repository again. Later stages stay fail-closed and must resume to deleted.
func (b *DurableBackend) ReapUncommittedDeletes(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !filepath.IsAbs(b.Root) || b.Effects == nil {
		return errors.New("repository backend is incomplete")
	}
	entries, err := os.ReadDir(b.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "delete-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		operationID := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "delete-"), ".json")
		if _, err := uuid.Parse(operationID); err != nil {
			continue
		}
		path := filepath.Join(b.Root, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var record deleteBackendRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("invalid repository deletion record %s: %w", entry.Name(), err)
		}
		if record.OperationID != operationID {
			return fmt.Errorf("repository deletion record %s conflicts with its filename", entry.Name())
		}
		if _, err := uuid.Parse(record.RealmID); err != nil {
			return fmt.Errorf("repository deletion record %s has invalid realm ID", entry.Name())
		}
		if _, err := uuid.Parse(record.RepoID); err != nil {
			return fmt.Errorf("repository deletion record %s has invalid repository ID", entry.Name())
		}
		switch record.Stage {
		case "allocated":
			if err := b.Effects.RestoreDelete(ctx, record.RepoID, record.OperationID); err != nil {
				return fmt.Errorf("restore uncommitted repository deletion %s: %w", operationID, err)
			}
		case "blocked", "withdrawn", "deleted":
			// Once authority withdrawal may have begun, restoring write access
			// would race a durable or partially published deletion.
		default:
			return fmt.Errorf("repository deletion record %s has invalid stage", entry.Name())
		}
	}
	return nil
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
	// The canonical ownership check deliberately precedes creation of the
	// durable delete record and every filesystem side effect. A read grant must
	// never be enough to install a blocking hook on another realm's repository.
	if err := b.Effects.AuthorizeDelete(ctx, repoID, realmID); err != nil {
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
			prepareErr := err
			if err := b.Effects.RestoreDelete(ctx, repoID, operationID); err != nil {
				return time.Time{}, fmt.Errorf("prepare repository deletion: %w; restore uncommitted preparation: %v", prepareErr, err)
			}
			return time.Time{}, prepareErr
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
