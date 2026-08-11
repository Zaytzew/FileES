package clientupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/durable"
	"filees/internal/releaseenvelope"
)

const stateSchema = 1

type State struct {
	Schema           int    `json:"schema"`
	HighestSequence  uint64 `json:"highest_sequence"`
	SecurityEpoch    uint64 `json:"security_epoch"`
	ReleaseID        string `json:"release_id,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
}

type StateStore struct{ Path string }

func (store StateStore) Load() (State, error) {
	data, err := os.ReadFile(store.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{Schema: stateSchema}, nil
		}
		return State{}, err
	}
	var state State
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode client update state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return State{}, errors.New("client update state contains trailing data")
	}
	if state.Schema != stateSchema {
		return State{}, fmt.Errorf("unsupported client update state schema %d", state.Schema)
	}
	return state, nil
}

func (store StateStore) Save(state State) error {
	state.Schema = stateSchema
	dir := filepath.Dir(store.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(dir, ".update-state-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, store.Path); err != nil {
		return err
	}
	// os.Open+Sync is POSIX-only: on Windows that handle is read-only, so
	// FlushFileBuffers refuses it with "Access is denied" and the update state
	// fails to persist. This runs on client machines, so Windows is not a
	// developer-only concern here.
	return durable.SyncDirectory(dir)
}

func (state State) Check(envelope *releaseenvelope.Envelope) error {
	if envelope == nil {
		return errors.New("release envelope is nil")
	}
	if envelope.SecurityEpoch < state.SecurityEpoch {
		return fmt.Errorf("security epoch rollback: %d < %d", envelope.SecurityEpoch, state.SecurityEpoch)
	}
	if envelope.Sequence < state.HighestSequence {
		return fmt.Errorf("release sequence rollback: %d < %d", envelope.Sequence, state.HighestSequence)
	}
	if envelope.Sequence == state.HighestSequence && state.ReleaseID != "" && envelope.ReleaseID != state.ReleaseID {
		return fmt.Errorf("release sequence fork: %s and %s use sequence %d", state.ReleaseID, envelope.ReleaseID, envelope.Sequence)
	}
	return nil
}

func (state State) Advance(envelope *releaseenvelope.Envelope, installedVersion string) (State, error) {
	if err := state.Check(envelope); err != nil {
		return State{}, err
	}
	if envelope.Sequence > state.HighestSequence {
		state.HighestSequence = envelope.Sequence
		state.ReleaseID = envelope.ReleaseID
	}
	if envelope.SecurityEpoch > state.SecurityEpoch {
		state.SecurityEpoch = envelope.SecurityEpoch
	}
	state.InstalledVersion = strings.TrimSpace(installedVersion)
	state.Schema = stateSchema
	return state, nil
}
