package servertool

import (
	"errors"
	"os"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

const realmRecoveryAdminGrace = 10 * 24 * time.Hour

// realmRecoveryPublisher materializes the complete download capability before
// the removal executor is allowed to revoke the realm's remaining clients.
type realmRecoveryPublisher struct {
	ArchiveRoot string
	Manifests   repoworker.RecoveryManifestStore
	Keys        repoworker.RecoveryKeyStore
	Grace       time.Duration
}

func (p realmRecoveryPublisher) Prepare(record repoworker.RealmRemovalRecord) error {
	if record.ConfirmedAt == nil || record.ConfirmedAt.IsZero() {
		return errors.New("realm removal confirmation time is missing")
	}
	if existing, err := p.Manifests.Load(record.OperationID); err == nil {
		if existing.RealmID != record.RealmID {
			return errors.New("recovery manifest belongs to another realm")
		}
		if len(existing.Archives) > 0 {
			_, err = p.Keys.Bind(existing, record.Request.RecoveryPublicKey)
		}
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	archives := make([]repoworker.RecoveryArchive, 0, len(record.Scope.OwnedRepoIDs))
	downloadUntil := record.ConfirmedAt.UTC()
	grace := p.Grace
	if grace <= 0 {
		grace = realmRecoveryAdminGrace
	}
	for _, repoID := range record.Scope.OwnedRepoIDs {
		deleteOperationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.OperationID+":"+repoID+":delete")).String()
		if _, _, err := repoworker.PromoteDeletionArchiveToRecovery(p.ArchiveRoot, repoID, deleteOperationID, grace); err != nil {
			return err
		}
		archive, retainedUntil, found, err := repoworker.DeletionRecoveryArchive(p.ArchiveRoot, repoID, deleteOperationID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		archives = append(archives, archive)
		if len(archives) == 1 || retainedUntil.Before(downloadUntil) {
			downloadUntil = retainedUntil
		}
	}
	manifest := repoworker.RecoveryManifest{
		Schema:          repoworker.RecoveryManifestSchema,
		OperationID:     record.OperationID,
		RealmID:         record.RealmID,
		Archives:        repoworker.SortedRecoveryArchives(archives),
		DownloadUntil:   downloadUntil,
		AdminGraceUntil: downloadUntil.Add(grace),
		CreatedAt:       record.ConfirmedAt.UTC(),
	}
	if err := p.Manifests.Save(manifest); err != nil {
		return err
	}
	if len(archives) > 0 {
		if _, err := p.Keys.Bind(manifest, record.Request.RecoveryPublicKey); err != nil {
			return err
		}
	}
	return nil
}
