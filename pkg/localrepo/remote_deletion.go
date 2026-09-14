package localrepo

import (
	"errors"
	"os"

	"github.com/google/uuid"
)

// ObserveRemoteDeletion durably fences every local attachment of this identity
// before its runtime is stopped. The caller must have authenticated the fact.
// It neither issues DELETE nor touches working copies, even dirty ones.
func (s *Store) ObserveRemoteDeletion(serverID, repoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := make(map[string]Record)
	after := make(map[string]Record)
	found := false
	for id, record := range s.records {
		if record.ServerID != serverID || record.RepoID != repoID {
			continue
		}
		found = true
		if record.RemoteDeletionObserved || record.State == StateDeleted {
			continue
		}
		// The initiating client's own durable delete/recovery workflow must
		// finish its existing receipts, not be replaced by a clone receipt.
		if record.State == StateDeleting {
			continue
		}
		if record.LocalPath == "" {
			return errors.New("remote deletion has no known local attachment")
		}
		before[id] = record
		record.State = StateDetached
		if record.DetachOperationID == "" {
			record.DetachOperationID = uuid.NewString()
		}
		record.RemoteDeletionObserved = true
		record.PreservedAlternatePath = record.PendingLocalPath
		record.PendingLocalPath = ""
		record.RelocationAdoptExisting = false
		record.RelocationMoveExisting = false
		record.ReconcileOperationID = ""
		record.LoadDumpApplyIgnorePolicy = false
		record.LoadDumpKeepLastRevisions = nil
		record.LastError = ""
		record.UpdatedAt = s.now().UTC()
		if err := validate(record); err != nil {
			return err
		}
		after[id] = record
	}
	if !found {
		return os.ErrNotExist
	}
	if len(before) == 0 {
		return nil
	}
	for id, record := range after {
		s.records[id] = record
	}
	if err := s.persist(); err != nil {
		for id, record := range before {
			s.records[id] = record
		}
		return err
	}
	return nil
}

func (s *Store) RemoteDeleted(serverID, repoID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.ServerID == serverID && record.RepoID == repoID && record.RemoteDeletionObserved {
			return true
		}
	}
	return false
}

// DismissRemoteDeletedCopy acknowledges all preserved copies only after their
// metadata cleanup succeeded. Keep the authority fence forever; never touch
// a folder already returned to its owner, even if it has since been reused.
func (s *Store) DismissRemoteDeletedCopy(serverID, repoID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := make(map[string]Record)
	var result Record
	for id, record := range s.records {
		if record.ServerID != serverID || record.RepoID != repoID {
			continue
		}
		if !record.RemoteDeletionObserved || !record.LocalCleanupCompleted {
			return Record{}, errors.New("local projection requires confirmed deletion and completed metadata cleanup")
		}
		result = record
		if !record.LocalProjectionDismissed {
			before[id] = record
		}
	}
	if result.OperationID == "" {
		return Record{}, os.ErrNotExist
	}
	if len(before) == 0 {
		return result, nil
	}
	for id, record := range before {
		record.LocalProjectionDismissed = true
		record.UpdatedAt = s.now().UTC()
		s.records[id] = record
	}
	if err := s.persist(); err != nil {
		for id, record := range before {
			s.records[id] = record
		}
		return Record{}, err
	}
	return s.records[result.OperationID], nil
}

// RecordPreservedCopyStatus stores a local-only inspection, never server
// authority. It may not reopen the attachment or touch the working copy.
func (s *Store) RecordPreservedCopyStatus(operationID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok || !record.RemoteDeletionObserved {
		return os.ErrNotExist
	}
	if record.RemoteCleanupStarted {
		return errors.New("inspection is frozen before metadata cleanup")
	}
	before := record
	record.PreservedCopyStatus = status
	if err := validate(record); err != nil {
		return err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return err
	}
	return nil
}

// BeginRemoteCleanup freezes the pre-cleanup inspection before .svn can go.
func (s *Store) BeginRemoteCleanup(operationID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok || !record.RemoteDeletionObserved {
		return os.ErrNotExist
	}
	if record.RemoteCleanupStarted {
		return nil
	}
	before := record
	record.PreservedCopyStatus = status
	record.RemoteCleanupStarted = true
	if err := validate(record); err != nil {
		return err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return err
	}
	return nil
}

func (s *Store) RecordRemoteCleanupError(operationID string, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok || !record.RemoteDeletionObserved || record.LocalCleanupCompleted {
		return errors.New("remote metadata cleanup is not pending")
	}
	before := record
	record.LastError = cause.Error()
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return err
	}
	return nil
}

func (s *Store) CompleteRemoteCleanup(operationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok || !record.RemoteDeletionObserved || !record.RemoteCleanupStarted {
		return errors.New("remote metadata cleanup has not started")
	}
	if record.LocalCleanupCompleted {
		return nil
	}
	before := record
	record.LocalCleanupCompleted = true
	record.LastError = ""
	if err := validate(record); err != nil {
		return err
	}
	s.records[operationID] = record
	if err := s.persist(); err != nil {
		s.records[operationID] = before
		return err
	}
	return nil
}

func remoteCleanupOverlaps(record Record, path string) bool {
	return path != "" && record.RemoteDeletionObserved && !record.LocalCleanupCompleted &&
		(pathsOverlap(record.LocalPath, path) || (record.PreservedAlternatePath != "" && pathsOverlap(record.PreservedAlternatePath, path)))
}

// Defend upgrades from an older client which could reuse a detached path
// before metadata cleanup existed. Never strip another live attachment.
func (s *Store) CheckRemoteCleanupPaths(operationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok || !record.RemoteDeletionObserved {
		return os.ErrNotExist
	}
	for id, other := range s.records {
		if id == operationID || terminal(other.State) {
			continue
		}
		if remoteCleanupOverlaps(record, other.LocalPath) || remoteCleanupOverlaps(record, other.PendingLocalPath) {
			return errors.New("metadata cleanup path is owned by another live attachment")
		}
	}
	return nil
}
