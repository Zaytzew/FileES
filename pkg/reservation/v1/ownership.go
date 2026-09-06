package v1

import (
	"encoding/hex"
	"errors"
	"path"
	"strings"

	"github.com/google/uuid"
)

func validateOwnership(r Result) error {
	fail := errors.New("invalid authoritative path ownership")
	seenLocks := map[string]bool{}
	for _, lock := range r.Reservations {
		if r.Schema == AutolockSchema {
			if lock.Path == "" || lock.Path == "." || path.IsAbs(lock.Path) || path.Clean(lock.Path) != lock.Path || lock.Path == ".." || strings.HasPrefix(lock.Path, "../") || strings.ContainsAny(lock.Path, "\\\x00\r\n") || lock.Token == "" || seenLocks[lock.Path] {
				return fail
			}
			seenLocks[lock.Path] = true
		}
		if lock.OwnerRealmID != "" {
			if r.Schema != AutolockSchema {
				return fail
			}
			if _, err := uuid.Parse(lock.OwnerRealmID); err != nil {
				return fail
			}
		}
	}
	if r.Schema != AutolockSchema {
		if r.PathOwnership != nil || r.OwnershipDetail != "" {
			return fail
		}
		return nil
	}
	if r.RepoID == "" {
		return fail
	}
	if r.RepositoryState == "deleted" {
		if r.PathOwnership != nil || r.OwnershipDetail != "" {
			return fail
		}
		return nil
	}
	if r.PathOwnership == nil {
		if strings.TrimSpace(r.OwnershipDetail) == "" {
			return fail
		}
		return nil // explicit unknown, never permission to use repo_owner
	}
	s := r.PathOwnership
	if r.OwnershipDetail != "" || r.Stale || r.Unknown || s.Revision < 0 {
		return fail
	}
	if _, err := uuid.Parse(s.RepositoryUUID); err != nil {
		return fail
	}
	seen := map[string]bool{}
	for _, e := range s.Entries {
		if e.Path == "" || e.Path == "." || strings.HasPrefix(e.Path, "/") || path.Clean(e.Path) != e.Path || e.Path == ".." || strings.HasPrefix(e.Path, "../") || strings.ContainsAny(e.Path, "\\\x00\r\n") || seen[e.Path] {
			return fail
		}
		seen[e.Path] = true
		id, err := hex.DecodeString(e.ID)
		if err != nil || len(id) != 32 || e.CreatedRevision < 1 || e.CreatedRevision > s.Revision || e.FirstCommitter == "" || (e.Kind != "file" && e.Kind != "dir") {
			return fail
		}
		if _, err := uuid.Parse(e.OwnerRealmID); err != nil {
			return fail
		}
	}
	return nil
}
