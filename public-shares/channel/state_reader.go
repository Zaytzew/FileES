package channel

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// ListOwnedRepositories reads the existing atomic channel records once for a
// server-supplied scope. Unlike mutation validation, this read needs neither
// a token key nor mkdir/write permission. Caller holds the service authority
// fence shared by channel mutations; this mutex only protects this Store.
func (s *Store) ListOwnedRepositories(ctx context.Context, realm string, repoIDs []string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !filepath.IsAbs(s.Root) || s.Authority == nil {
		return nil, errors.New("public share reader is incomplete")
	}
	if _, err := uuid.Parse(realm); err != nil {
		return nil, ErrForbidden
	}
	allowed := map[string]bool{}
	for _, id := range repoIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := uuid.Parse(id); err != nil {
			return nil, ErrForbidden
		}
		if err := s.Authority.OwnsActiveRepository(realm, id); err != nil {
			return nil, ErrForbidden
		}
		allowed[id] = true
	}
	if len(allowed) == 0 {
		return []Record{}, nil
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "channels"))
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []Record{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.loadPath(filepath.Join(s.Root, "channels", entry.Name()))
		if err != nil {
			return nil, err
		}
		if record.OwnerRealm == realm && allowed[record.RepoID] && record.State != StateDeleted {
			result = append(result, record)
		}
	}
	return result, nil
}
