package repoworker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

type FileStore struct {
	root string
	mu   sync.Mutex
}

func NewFileStore(root string) (*FileStore, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("repository result root must be absolute")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}
func (s *FileStore) path(id string, typ control.TicketType) (string, error) {
	if _, e := uuid.Parse(id); e != nil {
		return "", errors.New("operation ID must be UUID")
	}
	if typ != control.TicketStoragePreflight && typ != control.TicketCreateRepository && typ != control.TicketInitialCommit && typ != control.TicketMobilePairing {
		return "", errors.New("unsupported result ticket type")
	}
	return filepath.Join(s.root, id+"."+strings.ToLower(string(typ))+".json"), nil
}
func (s *FileStore) Load(id string, typ control.TicketType) (control.Result, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, e := s.path(id, typ)
	if e != nil {
		return control.Result{}, false, e
	}
	raw, e := os.ReadFile(p)
	if errors.Is(e, os.ErrNotExist) {
		return control.Result{}, false, nil
	}
	if e != nil {
		return control.Result{}, false, e
	}
	r, e := control.ParseResult(raw)
	return r, e == nil, e
}
func (s *FileStore) Save(r control.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := r.Validate(); e != nil {
		return e
	}
	p, e := s.path(r.OperationID, r.Type)
	if e != nil {
		return e
	}
	if raw, e := os.ReadFile(p); e == nil {
		old, e := control.ParseResult(raw)
		if e != nil {
			return e
		}
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(r)
		if string(a) == string(b) {
			return nil
		}
		return errors.New("conflicting result for operation")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	raw, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(s.root, ".result-*.tmp")
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
	if e = os.Rename(tmp, p); e != nil {
		return e
	}
	d, e := os.Open(s.root)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
