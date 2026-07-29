package servertool

import (
	"context"
	"errors"
	"fmt"

	"filees/pkg/repoworker"
)

// realmRemovalCoordinator derives all destructive targets from the
// authenticated server-side realm. It is intentionally not a control-ticket
// payload adapter: its two supplied readers are projections maintained by the
// server itself.
type realmRemovalCoordinator struct {
	Store         repoworker.RealmRemovalStore
	SnapshotScope func(string) (repoworker.RealmRemovalScope, error)
	ActiveClients func(string) ([]string, error)
	Execute       func(context.Context, repoworker.RealmRemovalRecord) error
	Manifests     repoworker.RecoveryManifestStore
}

func (c realmRemovalCoordinator) Request(ctx context.Context, session repoworker.Session, operationID string, request repoworker.RealmRemovalRequest) (repoworker.RealmRemovalRecord, error) {
	if err := ctx.Err(); err != nil {
		return repoworker.RealmRemovalRecord{}, err
	}
	if c.SnapshotScope == nil || c.ActiveClients == nil {
		return repoworker.RealmRemovalRecord{}, errors.New("realm removal scope services are unavailable")
	}
	scope, err := c.SnapshotScope(session.RealmID)
	if err != nil {
		return repoworker.RealmRemovalRecord{}, err
	}
	clients, err := c.ActiveClients(session.RealmID)
	if err != nil {
		return repoworker.RealmRemovalRecord{}, err
	}
	scope.ClientIDs = clients
	record, _, err := c.Store.BeginOperation(operationID, session.RealmID, scope, request)
	return record, err
}

func (c realmRemovalCoordinator) Confirm(ctx context.Context, session repoworker.Session, operationID, otp string) (repoworker.RealmRemovalRecord, repoworker.RecoveryManifest, error) {
	if err := ctx.Err(); err != nil {
		return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, err
	}
	record, err := c.Store.Load(operationID)
	if err != nil {
		return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, err
	}
	if record.RealmID != session.RealmID {
		return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, errors.New("realm removal operation does not belong to authenticated realm")
	}
	if record.State == repoworker.RealmRemovalAwaitingConfirmation {
		record, err = c.Store.Confirm(operationID, otp)
		if err != nil {
			return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, err
		}
	} else if record.State != repoworker.RealmRemovalDeleting && record.State != repoworker.RealmRemovalRecoveryReady && record.State != repoworker.RealmRemovalRevokingClients && record.State != repoworker.RealmRemovalCompleted {
		return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, fmt.Errorf("realm removal cannot resume from state %q", record.State)
	}
	if c.Execute == nil {
		return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, errors.New("realm removal executor is unavailable")
	}
	if err := c.Execute(ctx, record); err != nil {
		return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, err
	}
	record, err = c.Store.Load(operationID)
	if err != nil {
		return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, err
	}
	manifest, err := c.Manifests.Load(operationID)
	return record, manifest, err
}
