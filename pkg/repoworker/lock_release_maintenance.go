package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

const LockReleaseRetention = 7 * 24 * time.Hour

// Sweep reconciles private projections and removes terminal records only after
// retention AND authoritative disappearance of their exact token. Call under
// the worker and service-WC locks, also used by Request/Respond and publishing.
// Projection removal is committed before deleting the durable record; failed
// commits are retried, including after a restart with already modified views.
func (s *FileLockReleaseStore) Sweep(ctx context.Context, authority LockReleaseAuthority, publisher ServicePublisher) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if authority == nil {
		return 0, errors.New("lock release maintenance requires authority")
	}
	if err := s.ensureLocked(); err != nil {
		return 0, err
	}
	files, err := s.recordPathsLocked()
	if err != nil {
		return 0, err
	}
	removed := 0
	var failures []error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return removed, errors.Join(append(failures, err)...)
		}
		err := func() error {
			st, err := os.Lstat(file)
			if err != nil {
				return err
			}
			if !st.Mode().IsRegular() || st.Size() > 64<<10 {
				return errors.New("invalid lock release maintenance entry")
			}
			record, err := s.loadPathLocked(file)
			if err != nil {
				return err
			}
			if file != s.keyPath(record.RepoID, record.ObservedLockID, record.RequesterClientID) {
				return errors.New("lock release key mismatch")
			}
			observation, err := authority.InspectLock(ctx, record.RepoID, record.Path)
			if err != nil {
				// Only a canonical deletion is proof; missing files or an
				// unavailable svnadmin alone must never authorize collection.
				path, pathErr := repositoryRecordPath(publisher.ServiceWC, record.RepoID)
				raw, readErr := os.ReadFile(path)
				var repo repositoryRecord
				if pathErr != nil || readErr != nil || json.Unmarshal(raw, &repo) != nil || repo.Schema != RepositorySchema || repo.RepoID != record.RepoID || repo.State != "deleted" {
					return err
				}
				observation = nil
			}
			before := record
			record, err = s.expirePendingLocked(file, record)
			if err != nil {
				return err
			}
			if record.State == LockReleasePending {
				switch {
				case observation == nil:
					record.State = LockReleaseLockGone
				case observation.ObservedLockID != record.ObservedLockID:
					record.State = LockReleaseStale
				default:
					if err := validateHolderObservation(*observation, record.RequesterClientID); err != nil {
						return err
					}
					record.HolderClientID, record.HolderRealmID = observation.HolderClientID, observation.HolderRealmID
				}
				if record != before {
					record.UpdatedAt = s.now()
					if err := s.savePathLocked(file, record); err != nil {
						return err
					}
				}
			}
			sameToken := observation != nil && observation.ObservedLockID == record.ObservedLockID
			if record.State != LockReleasePending && !sameToken && !s.now().Before(record.UpdatedAt.Add(LockReleaseRetention)) && !s.now().Before(record.ExpiresAt) {
				if err := publisher.RemoveLockRelease(ctx, record); err != nil {
					return err
				}
				if err := os.Remove(file); err != nil {
					return err
				}
				removed++
				return syncDirectory(s.Root)
			}
			return publisher.publishLockRelease(ctx, record, false, true)
		}()
		if err != nil {
			failures = append(failures, fmt.Errorf("lock release maintenance: %w", err))
		}
	}
	return removed, errors.Join(failures...)
}
