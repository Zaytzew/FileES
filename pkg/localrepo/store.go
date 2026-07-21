// Package localrepo persists daemon-owned repository creation requests and
// local attachment intents. Server authority is deliberately not stored here.
package localrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const Schema = "filees.local-repository-lifecycle/v1"

type State string

const (
	StateRequestPending State = "request_pending"
	StateUnattached     State = "unattached"
	StatePolicyPending  State = "policy_pending"
	StateAttaching      State = "attaching"
	StateAttached       State = "attached"
	StateError          State = "error"
)

type Record struct {
	OperationID string    `json:"operation_id"`
	ServerID    string    `json:"server_id"`
	RepoID      string    `json:"repo_id,omitempty"`
	RepoURL     string    `json:"repo_url,omitempty"`
	Access      string    `json:"access,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	LocalPath   string    `json:"local_path"`
	State       State     `json:"state"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type document struct {
	Schema  string   `json:"schema"`
	Records []Record `json:"records"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	records map[string]Record
	now     func() time.Time
}

func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("local repository lifecycle path must be absolute")
	}
	s := &Store{path: filepath.Clean(path), records: make(map[string]Record), now: time.Now}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode local repository lifecycle: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("local repository lifecycle contains trailing data")
	}
	if doc.Schema != Schema {
		return nil, errors.New("local repository lifecycle schema is invalid")
	}
	for _, record := range doc.Records {
		if err := validate(record); err != nil {
			return nil, err
		}
		if _, exists := s.records[record.OperationID]; exists {
			return nil, errors.New("duplicate local repository operation")
		}
		s.records[record.OperationID] = record
	}
	return s, nil
}

func (s *Store) BeginCreate(serverID, displayName, localPath string) (Record, error) {
	return s.BeginCreateOperation(uuid.NewString(), serverID, displayName, localPath)
}

func (s *Store) BeginCreateOperation(operationID, serverID, displayName, localPath string) (Record, error) {
	return s.begin(Record{OperationID: operationID, ServerID: serverID, DisplayName: strings.TrimSpace(displayName), LocalPath: localPath, State: StateRequestPending})
}

func (s *Store) BeginAttach(serverID, repoID, localPath string, required bool) (Record, error) {
	state := StateUnattached
	if required {
		state = StatePolicyPending
	}
	return s.begin(Record{ServerID: serverID, RepoID: repoID, LocalPath: localPath, State: state})
}

func (s *Store) begin(record Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.OperationID == "" {
		record.OperationID = uuid.NewString()
	}
	record.LocalPath = filepath.Clean(record.LocalPath)
	record.CreatedAt = s.now().UTC()
	record.UpdatedAt = record.CreatedAt
	if err := validate(record); err != nil {
		return Record{}, err
	}
	for _, existing := range s.records {
		if existing.ServerID == record.ServerID && record.RepoID != "" && existing.RepoID == record.RepoID {
			return Record{}, errors.New("repository already has a local lifecycle record")
		}
		if pathsOverlap(existing.LocalPath, record.LocalPath) {
			return Record{}, errors.New("local path overlaps another FileES repository root")
		}
	}
	s.records[record.OperationID] = record
	if err := s.persist(); err != nil {
		delete(s.records, record.OperationID)
		return Record{}, err
	}
	return record, nil
}

func (s *Store) MarkAttached(operationID, repoID string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.State == StateAttached && record.RepoID == repoID {
			return nil
		}
		if record.State != StateRequestPending && record.State != StateAttaching && record.State != StateError {
			return errors.New("local repository operation cannot become attached")
		}
		if strings.TrimSpace(repoID) == "" || strings.ContainsAny(repoID, "/\\\x00\r\n\t ") {
			return errors.New("repository ID is invalid")
		}
		record.RepoID, record.State, record.LastError = repoID, StateAttached, ""
		return nil
	})
}

func (s *Store) ApproveAttach(operationID, serverID, repoID, repoURL, access string) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if record.ServerID != serverID || record.RepoID != repoID {
			return errors.New("attachment approval does not match the persisted intent")
		}
		if record.State == StateAttaching && record.RepoURL == repoURL && record.Access == access {
			return nil
		}
		if record.State != StateUnattached && record.State != StatePolicyPending && record.State != StateError {
			return errors.New("local repository operation cannot start attachment")
		}
		record.State, record.LastError, record.RepoURL, record.Access = StateAttaching, "", repoURL, access
		return nil
	})
}

func (s *Store) MarkError(operationID string, cause error) (Record, error) {
	return s.update(operationID, func(record *Record) error {
		if cause == nil || strings.TrimSpace(cause.Error()) == "" {
			return errors.New("local repository error is required")
		}
		record.State, record.LastError = StateError, cause.Error()
		return nil
	})
}

func (s *Store) update(operationID string, mutate func(*Record) error) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok {
		return Record{}, os.ErrNotExist
	}
	before := record
	if err := mutate(&record); err != nil {
		return Record{}, err
	}
	if record == before {
		return record, nil
	}
	record.UpdatedAt = s.now().UTC()
	if err := validate(record); err != nil {
		return Record{}, err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return Record{}, err
	}
	return record, nil
}

func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OperationID < out[j].OperationID })
	return out
}

func (s *Store) Get(operationID string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	return record, ok
}

func validate(r Record) error {
	if _, err := uuid.Parse(r.OperationID); err != nil {
		return errors.New("local repository operation ID must be UUID")
	}
	if strings.TrimSpace(r.ServerID) == "" || strings.ContainsAny(r.ServerID, "/\\\x00\r\n\t ") {
		return errors.New("local repository server ID is invalid")
	}
	if !filepath.IsAbs(r.LocalPath) || filepath.Clean(r.LocalPath) == string(filepath.Separator) {
		return errors.New("local repository path must be an absolute non-root path")
	}
	if r.RepoID == "" && r.DisplayName == "" {
		return errors.New("repository creation display name is required")
	}
	if r.RepoID != "" && strings.ContainsAny(r.RepoID, "/\\\x00\r\n\t ") {
		return errors.New("local repository ID is invalid")
	}
	if r.RepoURL != "" || r.Access != "" || r.State == StateAttaching {
		if !strings.HasPrefix(r.RepoURL, "svn+ssh://") || (r.Access != "r" && r.Access != "rw") {
			return errors.New("attached repository authority is invalid")
		}
	}
	switch r.State {
	case StateRequestPending, StateUnattached, StatePolicyPending, StateAttaching, StateAttached, StateError:
	default:
		return errors.New("local repository lifecycle state is invalid")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return errors.New("local repository lifecycle timestamps are invalid")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

func (s *Store) persist() error {
	doc := document{Schema: Schema, Records: make([]Record, 0, len(s.records))}
	for _, r := range s.records {
		doc.Records = append(doc.Records, r)
	}
	sort.Slice(doc.Records, func(i, j int) bool { return doc.Records[i].OperationID < doc.Records[j].OperationID })
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".lifecycle-*.tmp")
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
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
