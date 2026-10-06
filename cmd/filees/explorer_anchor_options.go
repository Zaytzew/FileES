//go:build !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"filees/internal/durable"
	contract "filees/pkg/contract/v1"
)

type anchorOptions struct {
	path    string
	enabled atomic.Bool
	readErr error
}
type anchorOptionsWire struct {
	Schema  string `json:"schema"`
	Enabled bool   `json:"enabled"`
}

func loadAnchorOptions(path string) *anchorOptions {
	options := &anchorOptions{path: path}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return options
	}
	if err != nil {
		options.readErr = err
		return options
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil || info.Size() > 4096 {
		options.readErr = errors.New("virtual folder preference exceeds limit")
		return options
	}
	var wire anchorOptionsWire
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&wire); err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF || wire.Schema != "filees.virtual-folders/v1" {
			err = errors.New("invalid virtual folder preference")
		}
	}
	options.readErr = err
	if err == nil {
		options.enabled.Store(wire.Enabled)
	}
	return options
}

func (m *anchorManager) virtualEnabled() bool { return m.options != nil && m.options.enabled.Load() }

// Changing this local preference shares the lifecycle-intent lock. Disabling
// never disconnects or purges points implicitly: detach them first using the
// existing choice that warns about keeping only downloaded files.
func (m *anchorManager) VirtualFolders(ctx context.Context, enabled *bool) (contract.VirtualFoldersStatus, error) {
	state := contract.VirtualFoldersStatus{Enabled: m.virtualEnabled()}
	if enabled == nil {
		return state, nil
	}
	if m.options == nil {
		return state, errors.New("virtual folders unavailable")
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if m.localIntentMu == nil || !m.localIntentMu.TryLock() {
		return state, errors.New("another local folder operation is running")
	}
	defer m.localIntentMu.Unlock()
	if m.options.readErr != nil {
		return state, errors.New("cannot read the saved virtual folder preference")
	}
	if *enabled == state.Enabled {
		return state, nil
	}
	if !*enabled {
		if m.lifecycle == nil {
			return state, errors.New("cannot confirm virtual folder retirement")
		}
		if err := m.lifecycle.CheckAnchorRetirement(); err != nil {
			return state, errors.New("detach all virtual folders first; keeping the folder preserves only downloaded files")
		}
	}
	if err := os.MkdirAll(filepath.Dir(m.options.path), 0700); err != nil {
		return state, err
	}
	file, err := os.CreateTemp(filepath.Dir(m.options.path), ".virtual-folders-*")
	if err != nil {
		return state, err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = json.NewEncoder(file).Encode(anchorOptionsWire{"filees.virtual-folders/v1", *enabled}); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, m.options.path)
	}
	if err != nil {
		return state, err
	}
	// Once replace succeeded, publish its actual value even if directory sync
	// fails: a lost ACK is never an invitation to silently repeat a decision.
	m.options.enabled.Store(*enabled)
	state.Enabled = *enabled
	return state, durable.SyncDirectory(filepath.Dir(m.options.path))
}
