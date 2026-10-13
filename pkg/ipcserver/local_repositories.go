package ipcserver

import (
	contract "filees/pkg/contract/v1"
)

// LocalRepositorySource supplies presentation-only durable local knowledge.
// It must not grant access, attach a runtime or resurrect a terminal intent.
type LocalRepositorySource interface {
	LocalRepositories() []contract.RepoSummary
}

func (s *Server) SetLocalRepositorySource(source LocalRepositorySource) {
	s.mu.Lock()
	s.localRepositories = source
	s.mu.Unlock()
}

func (s *Server) localRepositorySummaries() []contract.RepoSummary {
	s.mu.RLock()
	source := s.localRepositories
	s.mu.RUnlock()
	if source == nil {
		return nil
	}
	return source.LocalRepositories()
}

func (s *Server) mergeLocalRepositories(summaries []contract.RepoSummary) []contract.RepoSummary {
	indices := make(map[string]int, len(summaries))
	for i, row := range summaries {
		indices[row.ServerID+"\x00"+row.ID] = i
	}
	for _, local := range s.localRepositorySummaries() {
		if local.LocalCopy == nil || local.ID == "" || local.ServerID == "" {
			continue
		}
		if i, ok := indices[local.ServerID+"\x00"+local.ID]; ok {
			row := &summaries[i]
			if !row.ServerDeleted && !row.LocalCopyPreserved && (row.LocalPath == "" || row.LocalPath == local.LocalCopy.Path) {
				row.LocalCopy = local.LocalCopy
				if row.LocalPath == "" {
					row.LocalPath = local.LocalCopy.Path
				}
			}
			continue
		}
		// No runtime exists yet. This row is read-only local presentation;
		// all mutating commands still require the normal RepoState registry.
		local.Attached, local.Access, local.URL = false, "", ""
		local.State = contract.StateInitializing
		local.AttachmentPolicy = "optional"
		summaries = append(summaries, local)
		indices[local.ServerID+"\x00"+local.ID] = len(summaries) - 1
	}
	return summaries
}

func (s *Server) localRepositoryStatus(repoID string) *contract.RepoStatus {
	for _, row := range s.localRepositorySummaries() {
		if row.ID == repoID && row.LocalCopy != nil {
			return &contract.RepoStatus{RepoID: repoID, ServerID: row.ServerID,
				State: contract.StateInitializing, AttachmentPolicy: "optional"}
		}
	}
	return nil
}
