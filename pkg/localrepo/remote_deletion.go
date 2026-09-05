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

// RecordPreservedCopyStatus stores a local-only inspection, never server
// authority. It may not reopen the attachment or touch the working copy.
func (s *Store) RecordPreservedCopyStatus(operationID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[operationID]
	if !ok || !record.RemoteDeletionObserved {
		return os.ErrNotExist
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
