// Package anchorqueue stores confirmed local release intentions, not authority
// to change a repository. Exact selection and live attachment are rechecked by
// the existing batch executor before any effect.
package anchorqueue

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filees/internal/durable"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

const maxRecords = 128
const maxPending = 32

var ErrFull = errors.New("release queue is full")
var ErrConflict = errors.New("release confirmation identifier differs")
var ErrPoisoned = errors.New("release queue requires reload after a persistence failure")

type Intent struct {
	Payload     contract.RepoAnchorQueuePayload `json:"payload"`
	OperationID string                          `json:"operation_id"`
	RepoURL     string                          `json:"repo_url"`
	Root        string                          `json:"root"`
	CreatedAt   time.Time                       `json:"created_at"`
	Result      contract.RepoAnchorQueueResult  `json:"result"`
}

type document struct {
	Schema  string   `json:"schema"`
	Intents []Intent `json:"intents"`
}

type Store struct {
	mu       sync.Mutex
	path     string
	intents  []Intent
	poisoned bool
	faultAt  string
}

func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("release queue path must be absolute")
	}
	s := &Store{path: path}
	f, err := os.Open(path)
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
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("release queue has trailing data")
	}
	if doc.Schema != "filees.anchor-release-queue/v1" || len(doc.Intents) > maxRecords {
		return nil, errors.New("invalid release queue")
	}
	ids := map[string]bool{}
	recovering := false
	for i := range doc.Intents {
		x := &doc.Intents[i]
		if !valid(*x) || ids[x.Payload.IntentID] {
			return nil, errors.New("invalid release intent")
		}
		ids[x.Payload.IntentID] = true
		if x.Result.State == "running" {
			// The per-file release journal can reconcile its own receipt, but it
			// never grants a replay of this folder action after an uncertain stop.
			x.Result.State, x.Result.FailureCode, x.Result.FailureKey = "failed", "HEAD-2025", "head.release_queue_interrupted"
			x.Result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			recovering = true
		}
	}
	s.intents = doc.Intents
	if recovering {
		// Windows cannot atomically replace this file while our reader is open.
		if err := f.Close(); err != nil {
			return nil, err
		}
		if err := s.save(doc.Intents); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func valid(x Intent) bool {
	p := x.Payload
	raw, err := hex.DecodeString(p.Fingerprint)
	if id, e := uuid.Parse(p.IntentID); e != nil || id.String() != p.IntentID {
		return false
	}
	if err != nil || len(raw) != 32 || p.Action != "release" || p.ServerID == "" || p.RepoID == "" || p.Files <= 0 || p.Files > 20000 ||
		p.Path == "" || len(p.Path) > 32768 || x.OperationID == "" || x.RepoURL == "" || !filepath.IsAbs(x.Root) || x.CreatedAt.IsZero() {
		return false
	}
	if p.Path != "." {
		if strings.ContainsAny(p.Path, "\\:\x00\r\n\t") || filepath.ToSlash(filepath.Clean(p.Path)) != p.Path || filepath.IsAbs(p.Path) {
			return false
		}
		for _, part := range strings.Split(p.Path, "/") {
			if part == ".." || part == "." || part == "" || strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") || strings.TrimRight(part, " .") != part {
				return false
			}
		}
	}
	r := x.Result
	if r.IntentID != p.IntentID || r.ServerID != p.ServerID || r.RepoID != p.RepoID || r.LocalPath != x.Root || r.Path != p.Path ||
		r.Total != p.Files || r.Completed < 0 || r.Completed > r.Total {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, r.UpdatedAt); err != nil {
		return false
	}
	if r.State == "attached" && (r.Completed != r.Total || r.FailureCode != "" || r.FailureKey != "") {
		return false
	}
	if (r.State == "queued" || r.State == "running") && (r.Completed != 0 || r.FailureCode != "" || r.FailureKey != "") {
		return false
	}
	if r.State == "failed" && (r.FailureCode == "" || r.FailureKey == "") {
		return false
	}
	return r.State == "queued" || r.State == "running" || r.State == "attached" || r.State == "failed"
}

// Enqueue is idempotent even after completion. Only the caller's same immutable
// confirmation may reuse its ID; a manual retry requires a new confirmation.
func (s *Store) Enqueue(p contract.RepoAnchorQueuePayload, operationID, url, root string) (Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return Intent{}, ErrPoisoned
	}
	for _, x := range s.intents {
		if x.Payload.IntentID == p.IntentID {
			if x.Payload != p || x.OperationID != operationID || x.RepoURL != url || x.Root != root {
				return Intent{}, ErrConflict
			}
			return x, nil
		}
	}
	now := time.Now().UTC()
	x := Intent{Payload: p, OperationID: operationID, RepoURL: url, Root: root, CreatedAt: now,
		Result: contract.RepoAnchorQueueResult{IntentID: p.IntentID, ServerID: p.ServerID, RepoID: p.RepoID, UpdatedAt: now.Format(time.RFC3339Nano),
			RepoAnchorBatchResult: contract.RepoAnchorBatchResult{LocalPath: root, Path: p.Path, State: "queued", Total: p.Files}}}
	if !valid(x) {
		return Intent{}, errors.New("invalid release confirmation")
	}
	pending := 0
	next := append([]Intent(nil), s.intents...)
	for i := range next {
		old := &next[i]
		if old.Result.State == "queued" || old.Result.State == "running" {
			pending++
		} else if old.Payload.ServerID == p.ServerID && old.Payload.RepoID == p.RepoID && old.Payload.Path == p.Path {
			old.Result.Superseded = true // preserved receipt, no stale alarm after a fresh user confirmation
		}
	}
	if pending >= maxPending {
		return Intent{}, ErrFull
	}
	if len(next) >= maxRecords {
		// Retain unacknowledged failures and recent receipts. Only a superseded
		// terminal receipt older than a day can make room; never pending work.
		removed := false
		for i, old := range next {
			if old.Result.Superseded && old.Result.State != "queued" && old.Result.State != "running" && now.Sub(old.CreatedAt) > 24*time.Hour {
				next = append(next[:i:i], next[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			return Intent{}, ErrFull
		}
	}
	next = append(next, x)
	if err := s.save(next); err != nil {
		return Intent{}, err
	}
	s.intents = next
	return x, nil
}

func (s *Store) List() []Intent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Intent(nil), s.intents...)
}

// Fault is a presentation fence. The disk's pending evidence is not rewritten
// or replayed merely because a result cannot be durably stored.
func (s *Store) Fault() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.faultAt
}

func Diagnostic(err error) string {
	if err == nil {
		return ""
	}
	value := []rune(err.Error())
	if len(value) > 4096 {
		value = value[:4096]
	}
	return string(value)
}

func (s *Store) Set(id, expected string, result contract.RepoAnchorBatchResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrPoisoned
	}
	next := append([]Intent(nil), s.intents...)
	for i := range next {
		x := &next[i]
		if x.Payload.IntentID != id {
			continue
		}
		if x.Result.State != expected || (expected != "queued" && expected != "running") {
			return ErrConflict
		}
		if result.LocalPath == "" {
			result.LocalPath = x.Root
		}
		if result.Path == "" {
			result.Path = x.Payload.Path
		}
		result.Total = x.Payload.Files
		x.Result.RepoAnchorBatchResult = result
		x.Result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if !valid(*x) {
			return errors.New("invalid release outcome")
		}
		if err := s.save(next); err != nil {
			return err
		}
		s.intents = next
		return nil
	}
	return os.ErrNotExist
}

func (s *Store) save(next []Intent) (err error) {
	defer func() {
		if err != nil {
			s.poisoned = true
			s.faultAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}()
	raw, err := json.Marshal(document{Schema: "filees.anchor-release-queue/v1", Intents: next})
	if err != nil {
		return err
	}
	if len(raw) >= 1<<20 {
		return ErrFull
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".release-queue-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
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
	return durable.SyncDirectory(filepath.Dir(s.path))
}
