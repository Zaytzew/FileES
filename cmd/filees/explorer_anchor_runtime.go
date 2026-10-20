//go:build !nocfapi

package main

import (
	"context"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
)

// A missing/incompatible helper must not disappear into a startup warning.
// No provider or mutation service is installed on this path.
func (m *anchorManager) unavailableRuntime(detail string) func(context.Context) {
	if m.lifecycle == nil {
		return nil
	}
	return func(ctx context.Context) {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for ctx.Err() == nil {
			for _, record := range m.lifecycle.List() {
				if record.Anchor && record.State == localrepo.StateAttached {
					m.setRuntime(record, "failed", detail)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
}

func (m *anchorManager) clearRuntime(record localrepo.Record) {
	m.runtimeMu.Lock()
	delete(m.runtimeStatus, anchorKey(record))
	m.runtimeMu.Unlock()
	if m.guardChanged != nil {
		m.guardChanged(record.RepoID)
	}
}

// The source is read without the manager's lifecycle mutex: reconcile can
// publish it to a newly registered RepoState while holding that mutex.
func (m *anchorManager) runtimeOf(key string) *contract.AnchorRuntimeStatus {
	m.runtimeMu.Lock()
	defer m.runtimeMu.Unlock()
	value, ok := m.runtimeStatus[key]
	if !ok {
		return nil
	}
	return &value
}

func (m *anchorManager) publishRuntime(record localrepo.Record) {
	if m.repos == nil {
		return
	}
	if repo := m.repos(record.ServerID, record.RepoID); repo != nil {
		key := anchorKey(record)
		repo.SetAnchorRuntimeSource(func() *contract.AnchorRuntimeStatus { return m.runtimeOf(key) })
	}
}

func (m *anchorManager) setRuntime(record localrepo.Record, phase, detail string) {
	if len(detail) > 2048 {
		detail = detail[:2048]
	}
	key := anchorKey(record)
	m.runtimeMu.Lock()
	previous := m.runtimeStatus[key]
	// Background retry is not evidence of recovery. Keep the failure and its
	// original timestamp until the helper actually announces connected.
	if previous.Phase == "failed" && (phase == "preparing" || phase == "connecting") {
		m.runtimeMu.Unlock()
		m.publishRuntime(record)
		return
	}
	if previous.Phase == phase && previous.Detail == detail {
		m.runtimeMu.Unlock()
		m.publishRuntime(record)
		return
	}
	if m.runtimeStatus == nil {
		m.runtimeStatus = make(map[string]contract.AnchorRuntimeStatus)
	}
	m.runtimeStatus[key] = contract.AnchorRuntimeStatus{Phase: phase, Detail: detail, Since: time.Now().UTC().Format(time.RFC3339Nano)}
	m.runtimeMu.Unlock()
	m.publishRuntime(record)
	if m.guardChanged != nil {
		m.guardChanged(record.RepoID)
	}
}
