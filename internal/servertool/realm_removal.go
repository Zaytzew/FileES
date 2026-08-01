package servertool

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
	scope, err := c.snapshot(session.RealmID)
	if err != nil {
		return repoworker.RealmRemovalRecord{}, err
	}
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
		currentScope, err := c.snapshot(session.RealmID)
		if err != nil {
			return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, err
		}
		if !sameRealmRemovalScope(record.Scope, currentScope) {
			return repoworker.RealmRemovalRecord{}, repoworker.RecoveryManifest{}, errors.New("realm removal scope changed; start a new request")
		}
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

func (c realmRemovalCoordinator) snapshot(realmID string) (repoworker.RealmRemovalScope, error) {
	if c.SnapshotScope == nil || c.ActiveClients == nil {
		return repoworker.RealmRemovalScope{}, errors.New("realm removal scope services are unavailable")
	}
	scope, err := c.SnapshotScope(realmID)
	if err != nil {
		return repoworker.RealmRemovalScope{}, err
	}
	clients, err := c.ActiveClients(realmID)
	if err != nil {
		return repoworker.RealmRemovalScope{}, err
	}
	scope.ClientIDs = clients
	sort.Strings(scope.ClientIDs)
	sort.Strings(scope.OwnedRepoIDs)
	sort.Strings(scope.ForeignGrantRepoIDs)
	return scope, nil
}

func sameRealmRemovalScope(left, right repoworker.RealmRemovalScope) bool {
	return sameRealmRemovalIDs(left.ClientIDs, right.ClientIDs) &&
		sameRealmRemovalIDs(left.OwnedRepoIDs, right.OwnedRepoIDs) &&
		sameRealmRemovalIDs(left.ForeignGrantRepoIDs, right.ForeignGrantRepoIDs)
}

func sameRealmRemovalIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
