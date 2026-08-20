// Package whaleworker implements server-owned durable Whale operation state.
package whaleworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	whale "filees/pkg/whale/v1"
)

var ErrGenerationConflict = errors.New("Whale generation identity conflict")

type Journal struct {
	Root string
	Now  func() time.Time
}

type Record struct {
	Schema             string          `json:"schema"`
	Identity           whale.Identity  `json:"identity"`
	Direction          whale.Direction `json:"direction"`
	State              whale.State     `json:"state"`
	BytesHave          int64           `json:"bytes_have"`
	CommitBaseRevision int64           `json:"commit_base_revision,omitempty"`
	CommitBaseKnown    bool            `json:"commit_base_known,omitempty"`
	Revision           int64           `json:"revision,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

func (j Journal) Create(identity whale.Identity, direction whale.Direction) (Record, error) {
	if !filepath.IsAbs(j.Root) {
		return Record{}, errors.New("Whale journal root must be absolute")
	}
	if err := identity.Validate(); err != nil {
		return Record{}, err
	}
	state, err := whale.InitialState(direction)
	if err != nil {
		return Record{}, err
	}
	if current, err := j.Load(identity.GenerationID); err != nil {
		return Record{}, err
	} else if current != nil {
		if current.Identity != identity || current.Direction != direction {
			return Record{}, ErrGenerationConflict
		}
		return *current, nil
	}

	now := j.now().Format(time.RFC3339Nano)
	record := Record{Schema: whale.GenerationSchema, Identity: identity, Direction: direction, State: state, CreatedAt: now, UpdatedAt: now}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	if err := os.MkdirAll(j.Root, 0o700); err != nil {
		return Record{}, err
	}
	tmpDir, err := os.MkdirTemp(j.Root, ".generation-*.tmp")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(tmpDir)
	if err := writeRecord(filepath.Join(tmpDir, "state.json"), record); err != nil {
		return Record{}, err
	}
	if err := syncDir(tmpDir); err != nil {
		return Record{}, err
	}
	finalDir := j.generationDir(identity.GenerationID)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		// A concurrent creator may have won. Its immutable tuple decides
		// whether this invocation is an idempotent retry or a conflict.
		current, loadErr := j.Load(identity.GenerationID)
		if loadErr != nil {
			return Record{}, err
		}
		if current == nil {
			return Record{}, err
		}
		if current.Identity != identity || current.Direction != direction {
			return Record{}, ErrGenerationConflict
		}
		return *current, nil
	}
	if err := syncDir(j.Root); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (j Journal) Load(generationID string) (*Record, error) {
	if !filepath.IsAbs(j.Root) {
		return nil, errors.New("Whale journal root must be absolute")
	}
	// Identity validation also makes the generation safe as a path segment.
	probe := whale.Identity{LogicalRepoID: generationID, LogicalPath: "x", GenerationID: generationID, ExpectedSize: 1, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	if err := probe.Validate(); err != nil {
		return nil, errors.New("generation_id must be a canonical UUID")
	}
	raw, err := os.ReadFile(j.statePath(generationID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode Whale journal: %w", err)
	}
	if err := validateRecord(record); err != nil {
		return nil, fmt.Errorf("invalid Whale journal: %w", err)
	}
	if record.Identity.GenerationID != generationID {
		return nil, errors.New("invalid Whale journal: generation path mismatch")
	}
	return &record, nil
}

func (j Journal) PayloadPath(generationID string) (string, error) {
	if _, err := j.Load(generationID); err != nil {
		return "", err
	}
	return filepath.Join(j.generationDir(generationID), "payload.partial"), nil
}

// Save advances an existing generation. Identity and direction are immutable,
// offsets never decrease, and a state transition cannot skip integrity or
// commit verification stages.
func (j Journal) Save(next Record) error {
	current, err := j.Load(next.Identity.GenerationID)
	if err != nil {
		return err
	}
	if current == nil {
		return errors.New("Whale generation does not exist")
	}
	if current.Identity != next.Identity || current.Direction != next.Direction || current.CreatedAt != next.CreatedAt {
		return ErrGenerationConflict
	}
	if next.Revision < current.Revision || (current.Revision != 0 && next.Revision != current.Revision) {
		return errors.New("Whale revision is immutable once recorded")
	}
	if current.CommitBaseKnown && (!next.CommitBaseKnown || next.CommitBaseRevision != current.CommitBaseRevision) {
		return errors.New("Whale commit base revision is immutable once recorded")
	}
	if next.BytesHave < current.BytesHave {
		return errors.New("Whale durable offset cannot decrease")
	}
	if !whale.CanTransition(next.Direction, current.State, next.State) {
		return fmt.Errorf("invalid Whale state transition %s -> %s", current.State, next.State)
	}
	next.Schema = whale.GenerationSchema
	next.UpdatedAt = j.now().Format(time.RFC3339Nano)
	if err := validateRecord(next); err != nil {
		return err
	}
	return writeRecord(j.statePath(next.Identity.GenerationID), next)
}

func validateRecord(record Record) error {
	if record.Schema != whale.GenerationSchema {
		return errors.New("unsupported generation schema")
	}
	if err := record.Identity.Validate(); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, record.CreatedAt); err != nil {
		return errors.New("created_at is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.UpdatedAt); err != nil {
		return errors.New("updated_at is invalid")
	}
	if err := whale.ValidateOffset(record.BytesHave, record.Identity.ExpectedSize); err != nil {
		return err
	}
	initial, err := whale.InitialState(record.Direction)
	if err != nil {
		return err
	}
	known := record.State == initial || record.State == whale.StateRejected || record.State == whale.StateCancelled
	if record.Direction == whale.DirectionPut {
		known = known || record.State == whale.StateValidating || record.State == whale.StateCommitting || record.State == whale.StatePublished
	} else if record.Direction == whale.DirectionGet {
		known = known || record.State == whale.StateMaterializing || record.State == whale.StateVerifying || record.State == whale.StateLocal
	}
	if !known {
		return errors.New("state is invalid for Whale direction")
	}
	if record.Direction == whale.DirectionPut && (record.State == whale.StateValidating || record.State == whale.StateCommitting || record.State == whale.StatePublished) && record.BytesHave != record.Identity.ExpectedSize {
		return errors.New("PUT cannot leave receiving before all bytes are durable")
	}
	if record.Direction == whale.DirectionGet && (record.State == whale.StateVerifying || record.State == whale.StateLocal) && record.BytesHave != record.Identity.ExpectedSize {
		return errors.New("GET cannot verify before all bytes are durable")
	}
	if record.CommitBaseRevision < 0 || (!record.CommitBaseKnown && record.CommitBaseRevision != 0) || record.Revision < 0 || (record.State == whale.StatePublished && (!record.CommitBaseKnown || record.Revision == 0 || record.Revision <= record.CommitBaseRevision)) {
		return errors.New("published generation requires a positive revision")
	}
	return nil
}

func (j Journal) generationDir(generationID string) string {
	return filepath.Join(j.Root, generationID)
}

func (j Journal) statePath(generationID string) string {
	return filepath.Join(j.generationDir(generationID), "state.json")
}

func (j Journal) now() time.Time {
	if j.Now != nil {
		return j.Now().UTC()
	}
	return time.Now().UTC()
}

func writeRecord(path string, record Record) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDir(dir)
}
