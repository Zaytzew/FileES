package localrepo

import "errors"

// StartQueuedRelocation is a CAS on the selected attachment and destination.
// The caller holds the execution fence and has revalidated authority and
// pending local mutations. Recording a queue entry grants no such proof.
func (s *Store) StartQueuedRelocation(expected Record) (Record, error) {
	return s.update(expected.OperationID, func(record *Record) error {
		if !record.RelocationQueued || record.State != StateAttached ||
			record.ServerID != expected.ServerID || record.RepoID != expected.RepoID ||
			record.RepoURL != expected.RepoURL || record.LocalPath != expected.LocalPath ||
			record.PendingLocalPath != expected.PendingLocalPath || record.Access != expected.Access {
			return errors.New("queued relocation attachment changed")
		}
		record.State = StateRelocating
		return nil
	})
}

func (s *Store) DeferQueuedRelocation(expected Record) (Record, error) {
	return s.update(expected.OperationID, func(record *Record) error {
		if !record.RelocationQueued || record.State != StateRelocating ||
			record.LocalPath != expected.LocalPath || record.PendingLocalPath != expected.PendingLocalPath ||
			record.RepoURL != expected.RepoURL || record.RemoteDeletionObserved {
			return errors.New("queued relocation changed before deferral")
		}
		record.State = StateAttached
		return nil
	})
}
