package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"filees/pkg/clientview"
	"filees/public-shares/channel"
)

// The channel manifest is the sole authority for shelf ancestry. Rebuild also
// upgrades existing channels; no matching by display name, slug or local path.
func (p ServicePublisher) projectShelfParents(repositories map[string]repositoryRecord) error {
	if p.PublicShareStateRoot == "" {
		return nil
	}
	if !filepath.IsAbs(p.PublicShareStateRoot) {
		return errors.New("upload state root must be absolute")
	}
	for id, repo := range repositories {
		if repo.Purpose == clientview.PurposeUploadShelf {
			repo.ParentRepoID = ""
			repositories[id] = repo
		}
	}
	root := filepath.Join(p.PublicShareStateRoot, "upload-channels")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		var record channel.UploadRecord
		if json.Unmarshal(raw, &record) != nil || record.Schema != channel.UploadRecordSchema {
			return errors.New("invalid upload channel during shelf projection")
		}
		if record.Manifest == nil || record.State == channel.StateDeleted {
			continue
		}
		shelf, found := repositories[record.Manifest.UploadRepoID]
		parent, exists := repositories[record.Manifest.AuthorityRepoID]
		if !found || !exists {
			continue
		}
		if shelf.Purpose != clientview.PurposeUploadShelf || parent.Purpose != "" || shelf.OwnerRealmID != record.OwnerRealm || parent.OwnerRealmID != record.OwnerRealm || shelf.RepoID == parent.RepoID {
			return errors.New("upload shelf authority mismatch")
		}
		if shelf.ParentRepoID != "" && shelf.ParentRepoID != parent.RepoID {
			return errors.New("conflicting upload shelf parents")
		}
		shelf.ParentRepoID = parent.RepoID
		repositories[shelf.RepoID] = shelf
	}
	return nil
}

func (b *DurableBackend) RefreshUploadProjection(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	effects, ok := b.Effects.(interface{ RefreshUploadProjection(context.Context) error })
	if !ok {
		return errors.New("upload projection publisher unavailable")
	}
	return effects.RefreshUploadProjection(ctx)
}

func (e ServerEffects) RefreshUploadProjection(ctx context.Context) error {
	publisher, ok := e.Authority.(interface{ RebuildGrantAuthority(context.Context) error })
	if !ok {
		return errors.New("upload authority rebuild unavailable")
	}
	return publisher.RebuildGrantAuthority(ctx)
}
