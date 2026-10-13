package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
)

// localCopyPresentation reads the existing lifecycle, never server authority.
// Only root metadata is inspected: no tree walk, SVN invocation or hydration.
// Observations are bounded to current records and expire independently of
// server polls, so offline local folders remain visible as local folders.
type localCopyPresentation struct {
	store        *localrepo.Store
	active       func(string) bool
	mu           sync.Mutex
	observations map[string]localRootObservation
	now          func() time.Time
	stat         func(string) (os.FileInfo, error)
}

type localRootObservation struct {
	path, presence string
	at             time.Time
}

func (p *localCopyPresentation) LocalRepositories() []contract.RepoSummary {
	if p.store == nil {
		return nil
	}
	records := p.store.List()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.observations == nil {
		p.observations = make(map[string]localRootObservation)
	}
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	stat := os.Lstat
	if p.stat != nil {
		stat = p.stat
	}
	seen := make(map[string]bool)
	rows := make([]contract.RepoSummary, 0, len(records))
	for _, record := range records {
		if record.RepoID == "" || record.LocalPath == "" || record.RemoteDeletionObserved || record.ServerDeleteCompleted || record.LocalProjectionDismissed ||
			(p.active != nil && !p.active(record.ServerID)) {
			continue
		}
		switch record.State {
		case localrepo.StateAttached, localrepo.StateAttaching, localrepo.StateRelocating, localrepo.StateReconciling, localrepo.StateRepositoryCreated:
		default:
			continue
		}
		key := record.ServerID + "\x00" + record.RepoID
		seen[key] = true
		observation := p.observations[key]
		if observation.path != record.LocalPath || now.Sub(observation.at) >= 5*time.Second || observation.at.IsZero() {
			presence := "checking"
			// Do not synchronously probe explicit UNC paths. Their durable
			// location remains known, without guessing availability.
			if filepath.IsAbs(record.LocalPath) && !strings.HasPrefix(record.LocalPath, `\\`) && !strings.HasPrefix(record.LocalPath, "//") {
				presence = "unavailable"
				if info, err := stat(record.LocalPath); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					presence = "present"
				}
			}
			observation = localRootObservation{record.LocalPath, presence, now}
			p.observations[key] = observation
		}
		kind := "working_copy"
		if record.Anchor {
			kind = "virtual"
		} else if record.Sparse {
			kind = "sparse"
		}
		rows = append(rows, contract.RepoSummary{ID: record.RepoID, ServerID: record.ServerID,
			DisplayName: deletedRepositoryName(record), LocalPath: record.LocalPath,
			Purpose: record.Purpose, Sparse: record.Sparse,
			LocalCopy: &contract.LocalCopySummary{Path: record.LocalPath, Kind: kind,
				Presence: observation.presence, Lifecycle: string(record.State)}})
	}
	for key := range p.observations {
		if !seen[key] {
			delete(p.observations, key)
		}
	}
	return rows
}
