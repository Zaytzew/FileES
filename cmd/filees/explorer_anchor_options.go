//go:build !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"filees/internal/durable"
	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
)

type anchorOptions struct {
	path    string
	servers atomic.Value // immutable map[string]bool snapshots
	readErr error
}
type anchorOptionsWire struct {
	Schema  string          `json:"schema"`
	Enabled *bool           `json:"enabled,omitempty"` // legacy v1 only
	Servers map[string]bool `json:"servers,omitempty"`
}

const anchorOptionsLimit = 64 << 10

func validVirtualServerID(serverID string) bool {
	return serverID != "" && strings.TrimSpace(serverID) == serverID && !strings.ContainsAny(serverID, "/\\\x00\r\n\t ")
}

func loadAnchorOptions(path string, records ...localrepo.Record) *anchorOptions {
	options := &anchorOptions{path: path}
	options.servers.Store(map[string]bool{})
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return options
	}
	if err != nil {
		options.readErr = err
		return options
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil || info.Size() > anchorOptionsLimit {
		options.readErr = errors.New("virtual folder preference exceeds limit")
		return options
	}
	var wire anchorOptionsWire
	decoder := json.NewDecoder(io.LimitReader(file, anchorOptionsLimit+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&wire); err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF ||
			(wire.Schema != "filees.virtual-folders/v1" && wire.Schema != "filees.virtual-folders/v2") ||
			(wire.Schema == "filees.virtual-folders/v1" && (wire.Enabled == nil || wire.Servers != nil)) ||
			(wire.Schema == "filees.virtual-folders/v2" && wire.Enabled != nil) {
			err = errors.New("invalid virtual folder preference")
		}
	}
	if err == nil {
		servers := map[string]bool{}
		if wire.Schema == "filees.virtual-folders/v1" {
			// Capture once at startup. Preserve points, not a global opt-in.
			if *wire.Enabled {
				for _, record := range records {
					if record.NeedsAnchorRetirement() {
						servers[record.ServerID] = true
					}
				}
			}
		} else {
			for serverID, enabled := range wire.Servers {
				if !validVirtualServerID(serverID) {
					err = errors.New("invalid virtual folder server ID")
					break
				}
				if enabled {
					servers[serverID] = true
				}
			}
		}
		if err == nil {
			options.servers.Store(servers)
		}
	}
	options.readErr = err
	return options
}

func (m *anchorManager) virtualEnabled(serverID string) bool {
	return m.options != nil && m.options.servers.Load().(map[string]bool)[serverID]
}

// The lifecycle intent lock serialises this preference with point creation.
// Disabling never implicitly detaches points or checks another server.
func (m *anchorManager) VirtualFolders(ctx context.Context, serverID string, enabled *bool) (contract.VirtualFoldersStatus, error) {
	state := contract.VirtualFoldersStatus{ServerID: serverID, Enabled: m.virtualEnabled(serverID)}
	if m.options == nil {
		return state, errors.New("virtual folders unavailable")
	}
	if serverID == "" {
		if enabled != nil {
			return state, errors.New("server ID is required")
		}
		state.Servers = maps.Clone(m.options.servers.Load().(map[string]bool))
		return state, nil
	}
	// Unknown activations cannot create opt-ins for a later activation.
	if !validVirtualServerID(serverID) || !filepath.IsAbs(m.profiles) {
		return state, errors.New("server activation is unavailable")
	}
	dir, err := clientprofile.ServerDir(m.profiles, serverID)
	if err != nil {
		return state, err
	}
	profile, err := clientprofile.Load(filepath.Join(dir, "client-profile.json"))
	if err != nil || profile.ServerID != serverID {
		return state, errors.New("server activation is unavailable")
	}
	if enabled == nil {
		return state, nil
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if m.localIntentMu == nil || !m.localIntentMu.TryLock() {
		return state, errors.New("another local folder operation is running")
	}
	defer m.localIntentMu.Unlock()
	state.Enabled = m.virtualEnabled(serverID)
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
		if err := m.lifecycle.CheckServerAnchorRetirement(serverID); err != nil {
			return state, errors.New("detach this server's virtual folders first; keeping the folder preserves only downloaded files")
		}
	}
	servers := maps.Clone(m.options.servers.Load().(map[string]bool))
	if *enabled {
		servers[serverID] = true
	} else {
		delete(servers, serverID)
	}
	raw, err := json.Marshal(anchorOptionsWire{Schema: "filees.virtual-folders/v2", Servers: servers})
	if err != nil {
		return state, err
	}
	if len(raw)+1 > anchorOptionsLimit {
		return state, errors.New("virtual folder preference exceeds limit")
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
	if _, err = file.Write(append(raw, '\n')); err == nil {
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
	// Publish the durable value even if the directory ACK fails. Never retry.
	m.options.servers.Store(servers)
	state.Enabled = *enabled
	return state, durable.SyncDirectory(filepath.Dir(m.options.path))
}
