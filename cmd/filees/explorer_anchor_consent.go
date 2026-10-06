//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"sort"

	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
)

func (guard *anchorGuard) pending() []contract.AnchorDownloadBlock {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	var blocks []contract.AnchorDownloadBlock
	for _, block := range guard.blocks {
		blocks = append(blocks, block)
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].ID < blocks[j].ID })
	return blocks
}

func (guard *anchorGuard) decide(id string, allow bool) error {
	guard.mu.Lock()
	changed := false
	defer func() {
		guard.mu.Unlock()
		if changed && guard.changed != nil {
			guard.changed()
		}
	}()
	for process, block := range guard.blocks {
		if block.ID != id || !guard.refused[process] {
			continue
		}
		if allow {
			if guard.allowed == nil {
				guard.allowed = make(map[string]bool)
			}
			guard.allowed[process] = true
		}
		delete(guard.blocks, process) // refusal still holds, no repeated prompt
		changed = true
		return nil
	}
	return errors.New("download consent expired or belongs to a different provider session")
}

// Lifecycle serialisation prevents a decision crossing detach or relocation.
// The guard ID is newly generated for each refusal and never replayed by a
// fresh provider. Consent does not execute or retry any failed file request.
func (m *anchorManager) consent(ctx context.Context, p contract.RepoAnchorConsentPayload) error {
	if m.localIntentMu == nil {
		return errors.New("anchor lifecycle unavailable")
	}
	m.localIntentMu.Lock()
	defer m.localIntentMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, record := range m.lifecycle.List() {
		if record.ServerID != p.ServerID || record.RepoID != p.RepoID || !record.Anchor || record.State != localrepo.StateAttached {
			continue
		}
		m.mu.Lock()
		connection, ok := m.running[anchorKey(record)]
		m.mu.Unlock()
		if !ok || connection.ctx == nil || connection.ctx.Err() != nil || connection.providerCtx == nil || connection.providerCtx.Err() != nil || connection.repoURL != record.RepoURL || connection.bridge == nil || connection.bridge.guard == nil || connection.bridge.root != record.LocalPath {
			break
		}
		return connection.bridge.guard.decide(p.BlockID, p.Allow)
	}
	return errors.New("anchor attachment changed before consent")
}
