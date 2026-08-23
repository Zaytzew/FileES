package servertool

import (
	"context"
	"errors"
	"os"
	"time"

	"filees/pkg/repoworker"
)

// repositoryRecoveryPublisher turns one completed deletion receipt into the
// same key-pinned recovery capability used by realm removal. The manifest
// operation ID intentionally equals the original delete operation ID, which
// makes capability creation resumable even for deletions completed by an
// older client binary.
type repositoryRecoveryPublisher struct {
	Backend     *repoworker.DurableBackend
	ArchiveRoot string
	Manifests   repoworker.RecoveryManifestStore
	Keys        repoworker.RecoveryKeyStore
	Now         func() time.Time
}

func (p repositoryRecoveryPublisher) Prepare(_ context.Context, session repoworker.Session, operationID, repoID, publicKey string) (repoworker.RecoveryManifest, error) {
	if p.Backend == nil {
		return repoworker.RecoveryManifest{}, errors.New("repository recovery backend is unavailable")
	}
	receipt, err := p.Backend.DeletedRepository(operationID, session.RealmID, repoID)
	if err != nil {
		return repoworker.RecoveryManifest{}, err
	}
	if existing, err := p.Manifests.Load(operationID); err == nil {
		if existing.RealmID != session.RealmID || len(existing.Archives) > 1 || (len(existing.Archives) == 1 && existing.Archives[0].RepoID != repoID) {
			return repoworker.RecoveryManifest{}, errors.New("repository recovery manifest conflicts with deletion receipt")
		}
		if len(existing.Archives) > 0 {
			if _, err := p.Keys.Bind(existing, publicKey); err != nil {
				return repoworker.RecoveryManifest{}, err
			}
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return repoworker.RecoveryManifest{}, err
	}

	archive, downloadUntil, found, err := repoworker.DeletionRecoveryArchive(p.ArchiveRoot, repoID, operationID)
	if err != nil {
		return repoworker.RecoveryManifest{}, err
	}
	if downloadUntil.IsZero() {
		downloadUntil = receipt.RetainUntil
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	// A zero-retention deletion has no dump. Keep the receipt valid without
	// manufacturing a capability that could never serve an archive.
	createdAt := now
	if createdAt.After(downloadUntil) {
		createdAt = downloadUntil
	}
	manifest := repoworker.RecoveryManifest{
		Schema: repoworker.RecoveryManifestSchema, OperationID: operationID,
		RealmID: session.RealmID, CreatedAt: createdAt,
		DownloadUntil: downloadUntil, AdminGraceUntil: downloadUntil,
	}
	if found {
		manifest.Archives = []repoworker.RecoveryArchive{archive}
	}
	if err := p.Manifests.Save(manifest); err != nil {
		return repoworker.RecoveryManifest{}, err
	}
	if found {
		if _, err := p.Keys.Bind(manifest, publicKey); err != nil {
			return repoworker.RecoveryManifest{}, err
		}
	}
	return manifest, nil
}
