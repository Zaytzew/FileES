package localrepo

import (
	"errors"
	"path/filepath"

	"github.com/google/uuid"
)

// QueueConversion atomically reserves the original root AND its retained
// sibling using the existing queued relocation mechanism. The journal must
// already exist; an orphan waiting journal grants no execution authority.
func (s *Store) QueueConversion(expected Record, intent ConversionRecord) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[expected.OperationID]
	if !ok || r != expected {
		return Record{}, errors.New("conversion attachment changed before reservation")
	}
	if err := validateConversion(intent); err != nil {
		return Record{}, err
	}
	if !conversionMatchesAttachment(intent, r) || intent.Phase != ConversionWaiting || intent.Prepared {
		return Record{}, errors.New("conversion intent is not bound to this attachment")
	}
	if r.ConversionID == intent.ID {
		return r, nil
	}
	if s.anchorsRetired || r.State != StateAttached || r.ConversionID != "" || r.Sparse || r.Anchor || r.Purpose != "" || r.RelocationQueued || r.LastError != "" {
		return Record{}, errors.New("conversion requires an available ordinary attached working copy")
	}
	for id, other := range s.records {
		if remoteCleanupOverlaps(other, intent.BackupPath) {
			return Record{}, errors.New("conversion recovery path has pending metadata cleanup")
		}
		if id != r.OperationID && !terminal(other.State) &&
			(conversionPathsOverlap(other.LocalPath, intent.BackupPath) || (other.PendingLocalPath != "" && conversionPathsOverlap(other.PendingLocalPath, intent.BackupPath))) {
			return Record{}, errors.New("conversion recovery path overlaps another repository")
		}
	}
	before := r
	r.ConversionID, r.PendingLocalPath, r.RelocationQueued, r.RelocationMoveExisting = intent.ID, intent.BackupPath, true, true
	r.UpdatedAt = s.now().UTC()
	if err := validate(r); err != nil {
		return Record{}, err
	}
	s.records[r.OperationID] = r
	if err := s.persist(); err != nil {
		s.records[r.OperationID] = before
		return Record{}, err
	}
	return r, nil
}

func (s *Store) StartConversion(expected Record) (Record, error) {
	return s.update(expected.OperationID, func(r *Record) error {
		if *r != expected || r.ConversionID == "" || r.State != StateAttached {
			return errors.New("conversion reservation changed before execution")
		}
		r.State = StateRelocating
		return nil
	})
}

// FinishConversion is only a receipt projection: the executor has already
// proved the terminal journal outcome. Never infer success from a present path.
func (s *Store) FinishConversion(expected Record, outcome ConversionRecord) (Record, error) {
	if err := validateConversion(outcome); err != nil {
		return Record{}, err
	}
	return s.update(expected.OperationID, func(r *Record) error {
		if *r != expected || r.ConversionID != outcome.ID || !conversionMatchesAttachment(outcome, *r) || !conversionTerminal(outcome.Phase) {
			return errors.New("conversion completion does not match its lifecycle fence")
		}
		if outcome.Phase == ConversionComplete && r.State != StateRelocating {
			return errors.New("conversion did not enter its execution fence")
		}
		r.Anchor = outcome.Phase == ConversionComplete
		r.Sparse, r.SparsePath = r.Anchor, ""
		r.State, r.ConversionID, r.PendingLocalPath = StateAttached, "", ""
		r.RelocationQueued, r.RelocationMoveExisting, r.RelocationAdoptExisting = false, false, false
		r.LastError, r.LastErrorKey = "", ""
		return nil
	})
}

func (s *Store) RecordConversionError(operationID, conversionID string, cause error) (Record, error) {
	return s.update(operationID, func(r *Record) error {
		if r.ConversionID == "" || r.ConversionID != conversionID {
			return errors.New("conversion error has no matching reservation")
		}
		r.LastError, r.LastErrorKey = "", ""
		if cause != nil {
			r.LastError = cause.Error()
		}
		return nil
	})
}

// Waiting may restore only the ORIGINAL runtime, before any retained-source
// receipt. Ordinary relocation deferral must not reopen a partially converted WC.
func (s *Store) DeferConversion(expected Record, intent ConversionRecord) (Record, error) {
	if err := validateConversion(intent); err != nil {
		return Record{}, err
	}
	return s.update(expected.OperationID, func(r *Record) error {
		if *r != expected || r.ConversionID != intent.ID || !conversionMatchesAttachment(intent, *r) || intent.Prepared || intent.Phase != ConversionWaiting || r.State != StateRelocating {
			return errors.New("conversion has progressed beyond safe writer deferral")
		}
		r.State = StateAttached
		return nil
	})
}

func conversionMatchesAttachment(intent ConversionRecord, r Record) bool {
	return intent.AttachmentID == r.OperationID && intent.ServerID == r.ServerID && intent.RepoID == r.RepoID &&
		intent.RepoURL == r.RepoURL && intent.SourcePath == r.LocalPath &&
		intent.BackupPath == filepath.Join(filepath.Dir(r.LocalPath), "."+filepath.Base(r.LocalPath)+"-converting")
}

func validateConversionLifecycle(r Record) error {
	if r.ConversionID == "" {
		return nil
	}
	if uuid.Validate(r.ConversionID) != nil {
		return errors.New("invalid lifecycle conversion ID")
	}
	backup := filepath.Join(filepath.Dir(r.LocalPath), "."+filepath.Base(r.LocalPath)+"-converting")
	if r.RemoteDeletionObserved {
		if r.PreservedAlternatePath != backup || r.LocalCleanupCompleted {
			return errors.New("withdrawn conversion must retain both recovery paths")
		}
		return nil // global deletion fence wins; no conversion or normal cleanup may run
	}
	if (r.State != StateAttached && r.State != StateRelocating) || !r.RelocationQueued || !r.RelocationMoveExisting ||
		r.RelocationAdoptExisting || r.PendingLocalPath != backup || r.Anchor || r.Sparse || r.Purpose != "" {
		return errors.New("conversion must own its queued relocation fence")
	}
	return nil
}
