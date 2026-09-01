// Package reservationprojection persists the FileES server's last known-good
// reservation (lock) listing per repository, so a transient authority
// refresh failure can be answered from the last confirmed state instead of
// a silent false "0 reservations" or an unrecoverable blackout of every
// client asking about that repository. This lives on the remote FileES
// server (internal/servertool), not the desktop daemon: the daemon and GUI
// are transport/presentation, never a second source of this artifact. See
// concepts/RESERVATION_LISTING_RESILIENCE_CONCEPT.md and
// concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md §"Granica procesu".
//
// This package has no IPC, no SSH and no SVN — it only knows how to keep
// one JSON artifact per repository durable, atomic, and safe under
// concurrent refreshes from independent OS processes: every SSH session
// runs its own filees-serving-state process (see
// internal/servertool/client_entry.go's ClientReservationCommand), so an
// in-memory mutex alone cannot serialize refreshes — only a real,
// cross-process file lock can.
package reservationprojection

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filees/internal/durable"
	"filees/pkg/repoworker"
	reservationv1 "filees/pkg/reservation/v1"
)

const Schema = "filees.reservation-projection/v1"

// Artifact is the last confirmed reservation listing for one repository.
type Artifact struct {
	Schema       string                      `json:"schema"`
	RepoID       string                      `json:"repo_id"`
	Generation   int64                       `json:"generation"`
	AsOf         time.Time                   `json:"as_of"`
	Reservations []reservationv1.Reservation `json:"reservations"`
}

// Store keeps one atomically-replaced artifact file per repository under
// dir. The zero value is not usable; construct with NewStore.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir. dir is created on first Refresh if
// it does not yet exist; Load on a missing directory is simply "no artifact".
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Load returns the last persisted artifact for repoID. ok is false when no
// artifact has ever been written for this repository (a normal, expected
// "unknown" state, not an error). err is non-nil only when a file exists
// but could not be read back as a valid artifact — callers must treat that
// as corruption (log LOCK-2105 / reservation.projection_corrupt) and fail
// closed exactly as if ok were false, never trust a partially-parsed value.
//
// Load does not take the flock: Refresh's atomic temp+rename already
// guarantees any concurrent Load either sees the complete prior artifact or
// the complete new one, never a partial write, so a pure read needs no
// cross-process serialization of its own.
func (s *Store) Load(repoID string) (Artifact, bool, error) {
	path, _, err := s.pathsFor(repoID)
	if err != nil {
		return Artifact{}, false, err
	}
	return s.load(path, repoID)
}

func (s *Store) load(path, repoID string) (Artifact, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Artifact{}, false, nil
		}
		return Artifact{}, false, err
	}
	defer f.Close()
	var art Artifact
	decoder := json.NewDecoder(io.LimitReader(f, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&art); err != nil {
		return Artifact{}, false, err
	}
	if art.Schema != Schema || art.RepoID != repoID {
		return Artifact{}, false, errors.New("reservation projection artifact schema or repo_id mismatch")
	}
	return art, true, nil
}

// Refresh is the only way to write an artifact. It holds an exclusive,
// cross-process flock (repoworker.WithFileLock) on this repository's own
// lock file across the whole read-compute-write sequence, so two
// independent filees-serving-state processes refreshing the same
// repository at once can never observe the same prior generation and race
// each other to publish a duplicate or lower one — compute is only ever
// invoked with the generation of the artifact that is actually still on
// disk at the moment it runs, not one read earlier by a since-finished
// concurrent process. An in-process sync.Mutex was tried first and is not
// sufficient here: every request is its own OS process (see the package
// doc comment), so only flock(2) actually serializes them.
//
// compute receives the previous artifact (zero value, ok=false on first
// ever refresh) and returns the reservations to publish; Refresh fills in
// Schema/RepoID/Generation/AsOf.
func (s *Store) Refresh(repoID string, compute func(prev Artifact, ok bool) ([]reservationv1.Reservation, error)) (Artifact, error) {
	if strings.TrimSpace(repoID) == "" {
		return Artifact{}, errors.New("reservation projection refresh requires a repo id")
	}
	path, lockPath, err := s.pathsFor(repoID)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return Artifact{}, err
	}
	var result Artifact
	lockErr := repoworker.WithFileLock(lockPath, func() error {
		prev, ok, loadErr := s.load(path, repoID)
		if loadErr != nil {
			// A corrupt prior artifact must not block publishing a fresh
			// one; the caller is expected to have already logged the
			// corruption via a plain Load call before deciding to refresh.
			prev, ok = Artifact{}, false
		}
		reservations, err := compute(prev, ok)
		if err != nil {
			return err
		}
		next := Artifact{
			Schema:       Schema,
			RepoID:       repoID,
			Generation:   prev.Generation + 1,
			AsOf:         time.Now().UTC(),
			Reservations: append([]reservationv1.Reservation(nil), reservations...),
		}
		if err := s.save(path, next); err != nil {
			return err
		}
		result = next
		return nil
	})
	if lockErr != nil {
		return Artifact{}, lockErr
	}
	return result, nil
}

func (s *Store) save(path string, next Artifact) error {
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".reservation-projection-*.tmp")
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
	return durable.SyncDirectory(s.dir)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// pathsFor maps repoID onto its artifact path and its flock path, both
// derived from a filename with no collision risk. FileES repository ids
// are UUIDs (already a safe, fixed-shape filename) in every call site that
// constructs one; a UUID-shaped id is used verbatim. Any other value —
// never expected from this codebase's own callers, but never trusted
// blindly either — is hex-encoded byte-for-byte, which is reversible and
// collision-free by construction (unlike replacing disallowed characters
// with '_', which collapses distinct ids such as "a:b" and "a_b" onto the
// same file). Load still separately verifies the full RepoID recorded
// inside the artifact, as a second, independent check.
func (s *Store) pathsFor(repoID string) (artifactPath, lockPath string, err error) {
	if repoID == "" {
		return "", "", errors.New("reservation projection requires a non-empty repo id")
	}
	name := repoID
	if !uuidPattern.MatchString(repoID) {
		name = "hex-" + fmt.Sprintf("%x", repoID)
	}
	return filepath.Join(s.dir, name+".json"), filepath.Join(s.dir, "."+name+".flock"), nil
}
