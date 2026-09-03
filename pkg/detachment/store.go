// Package detachment records the moment a relationship with a server ended.
//
// FileES had no place for this. Every user-facing chronology the client can
// build is derived from the view model, and its three sources are all scoped
// to a repository: activity carries a RepoID and a path, errors are written
// into the repository's own errors.jsonl, and notices live inside the
// repository as shout records. A detached server has no repositories left in
// the view - that is the whole point of detaching - so none of the three can
// hold the one sentence the owner actually wants to read afterwards.
//
// pkg/opjournal is the daemon's own log and is deliberately not it either: it
// is an errmap sink, and a detachment is not a failure. Choosing on purpose is
// not an error even when it is irreversible.
//
// So the record is kept here, and kept durably, because the two things it has
// to feed both need it to survive a daemon restart. A journal entry is written
// once at the moment it happens; a "recently detached" panel forgets after
// about forty-eight hours, and a forty-eight hour lifetime that resets every
// time the desktop pair is rebuilt is not a lifetime at all.
//
// Everything a reader will need is copied in at the moment of recording rather
// than looked up later. After a detachment the client profile is gone and the
// server's own names are unreachable by definition, so a record that stored
// only an ID would render as a UID next to a date - which is A12 in the seam
// register, arrived at from the other direction.
package detachment

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/internal/durable"
)

const Schema = "filees.server-detachment/v1"

// Visibility is how long a detachment stays readable. The owner asked for
// "about forty-eight hours": long enough that a detachment made on Friday
// evening is still explainable on Sunday, short enough that the names do not
// become permanent furniture. Nothing here is load-bearing on the exact value.
const Visibility = 48 * time.Hour

// Cause distinguishes the two detachments that reach the same end state by
// opposite routes. They are not interchangeable and the cures differ: one is
// finished business, the other needs the client activated again.
type Cause string

const (
	// CauseSelf: the owner detached from the server here, confirming it. The
	// moment is exact, because we are the ones who chose it.
	CauseSelf Cause = "self"
	// CauseRevoked: the server refused this client - somebody deactivated it
	// there. The moment is when we first noticed, never when it was decided,
	// and the wording that reaches the reader must not pretend otherwise.
	CauseRevoked Cause = "revoked"
)

type Record struct {
	ServerID    string `json:"server_id"`
	DisplayName string `json:"display_name,omitempty"`
	Address     string `json:"address,omitempty"`
	Cause       Cause  `json:"cause"`
	// At is when this was recorded. For CauseRevoked that is detection time.
	At time.Time `json:"at"`
	// WorkingCopies are the local filesystem paths that belonged to this
	// server, captured now because nothing else will hold them later.
	//
	// The owner chose paths over repository names deliberately: after a
	// detachment the files are still on this disk, and where they are is the
	// only question left that has a useful answer.
	WorkingCopies []string `json:"working_copies,omitempty"`
	// ReattachedAt is set when the client became one of this server's own
	// again, and the record is kept rather than deleted.
	//
	// The two readers want opposite things here. A "recently detached" panel
	// describes how things stand, so a server back in the projection must not
	// also sit in it - the reader would have to decide which half to believe.
	// The journal is a chronology of what happened, and the detachment did
	// happen; deleting the entry because circumstances later changed would
	// make the record edit itself, which is the one thing a record must never
	// do.
	ReattachedAt time.Time `json:"reattached_at,omitempty"`
}

// Current reports whether this detachment still describes the present.
func (r Record) Current() bool { return r.ReattachedAt.IsZero() }

// Name is what a reader should be shown. A record that lost its display name
// still identifies its server rather than rendering as nothing.
func (r Record) Name() string {
	if name := strings.TrimSpace(r.DisplayName); name != "" {
		return name
	}
	return r.ServerID
}

type document struct {
	Schema  string   `json:"schema"`
	Records []Record `json:"records"`
}

// Store owns one small JSON document. Writes are whole-file and fsynced
// through internal/durable, matching pkg/localrepo: the records are few, they
// change rarely, and a torn file here would lose the only copy of a moment
// that cannot be reconstructed from anywhere else.
type Store struct {
	mu      sync.Mutex
	path    string
	records []Record
	now     func() time.Time
}

// Open loads the store at path, creating nothing until the first write.
func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("detachment store path must be absolute")
	}
	s := &Store{path: filepath.Clean(path), now: time.Now}
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
		return nil, err
	}
	if doc.Schema != Schema {
		return nil, errors.New("unexpected server detachment schema")
	}
	s.records = doc.Records
	return s, nil
}

// SetClock is for tests only.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Record stores one detachment, replacing any earlier record for the same
// server.
//
// Replacing rather than appending is the honest shape: a server can only be
// detached from once at a time, and a second detachment after a re-activation
// describes the current situation while the older one describes a relationship
// that has since been formed and ended again. Keeping both would put two
// contradictory rows on screen for one server.
func (s *Store) Record(rec Record) error {
	if strings.TrimSpace(rec.ServerID) == "" {
		return errors.New("detachment record needs a server id")
	}
	if rec.Cause != CauseSelf && rec.Cause != CauseRevoked {
		return errors.New("detachment record needs a known cause")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.At.IsZero() {
		rec.At = s.now()
	}
	rec.At = rec.At.UTC()
	kept := s.records[:0:0]
	for _, existing := range s.records {
		if existing.ServerID != rec.ServerID {
			kept = append(kept, existing)
		}
	}
	s.records = append(kept, rec)
	return s.persistLocked()
}

// RecordFirstNoticed stores rec only if this server has no record with this
// cause already, and reports whether anything was written.
//
// This is what the revocation path calls, and the difference from Record is
// the whole point. A revoked client is discovered by polling, so the same
// refusal arrives every cycle for as long as it lasts; recording each one
// would slide the moment forward minute by minute and the forty-eight hour
// lifetime would never expire. The honest answer is when we first noticed, and
// noticing happens once.
//
// A record with a different cause is replaced, because that is a genuine
// change of story rather than the same one repeated.
func (s *Store) RecordFirstNoticed(rec Record) (bool, error) {
	if strings.TrimSpace(rec.ServerID) == "" {
		return false, errors.New("detachment record needs a server id")
	}
	s.mu.Lock()
	for _, existing := range s.records {
		if existing.ServerID == rec.ServerID && existing.Cause == rec.Cause {
			s.mu.Unlock()
			return false, nil
		}
	}
	s.mu.Unlock()
	return true, s.Record(rec)
}

// Reattached marks serverID's detachment as no longer current, and reports
// whether anything changed.
//
// This is what a successful refresh calls, and it deliberately marks rather
// than deletes. The panel stops showing the row because the panel describes how
// things stand; the journal keeps the entry because the detachment happened and
// a later re-activation does not un-happen it. Deleting here would satisfy the
// first reader by lying to the second.
func (s *Store) Reattached(serverID string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() {
		at = s.now()
	}
	changed := false
	for i, existing := range s.records {
		if existing.ServerID == serverID && existing.ReattachedAt.IsZero() {
			s.records[i].ReattachedAt = at.UTC()
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, s.persistLocked()
}

// List returns the records still within Visibility, newest first, including
// ones already superseded by a re-activation.
//
// Both readers are served from this one list and neither is filtered here:
// which of them wants a superseded record is a presentation question, and the
// answers differ. Expiry is applied on the way out and persisted on the way
// past, so a daemon that runs for a week does not accumulate and a reader never
// sees a row that has outlived its lifetime.
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(s.now())
}

// ListAt is the deterministic form used by tests.
func (s *Store) ListAt(now time.Time) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(now)
}

func (s *Store) listLocked(now time.Time) []Record {
	kept := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		if now.Sub(rec.At) < Visibility {
			kept = append(kept, rec)
		}
	}
	if len(kept) != len(s.records) {
		s.records = append(s.records[:0:0], kept...)
		// A failed prune must not cost the caller its answer: the records are
		// correct in memory either way, and the file is rewritten on the next
		// write. Reporting an error from a read would make every consumer
		// handle a fault that changes nothing they can see.
		_ = s.persistLocked()
	}
	out := append([]Record(nil), kept...)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ServerID < out[j].ServerID
	})
	return out
}

func (s *Store) persistLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if len(s.records) == 0 {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return durable.SyncDirectory(dir)
	}
	raw, err := json.MarshalIndent(document{Schema: Schema, Records: s.records}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".detachments-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}
