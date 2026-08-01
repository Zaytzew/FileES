package servertool

import (
	"context"
	"errors"
	"time"

	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

// realmRemovalExecutor drives only immutable IDs stored in the journal. Each
// boundary is advanced after its effects, making a repeated confirmation safe
// after an interrupted worker process.
type realmRemovalExecutor struct {
	Store          repoworker.RealmRemovalStore
	Backend        realmRemovalBackend
	Recovery       realmRemovalRecoveryPublisher
	Publisher      realmRemovalGrantPublisher
	Activation     realmRemovalRevoker
	Erasure        realmRemovalErasure
	ErasureMaxDays int
}

type realmRemovalBackend interface {
	Delete(context.Context, string, string, string) (time.Time, error)
}
type realmRemovalGrantPublisher interface {
	WithdrawRealmGrants(context.Context, string, []string) error
}
type realmRemovalRecoveryPublisher interface {
	Prepare(repoworker.RealmRemovalRecord) error
}
type realmRemovalRevoker interface {
	FenceRealmRemoval(string, string, []string) error
	RevokeRealmRemoval(context.Context, string, string, []string, string) ([]string, error)
}
type realmRemovalErasure interface {
	Accept(repoworker.RealmRemovalRecord, int) (repoworker.DataErasureRecord, error)
	MarkActiveDataDeleted(string) (repoworker.DataErasureRecord, error)
}

func (e realmRemovalExecutor) Execute(ctx context.Context, record repoworker.RealmRemovalRecord) error {
	if e.Backend == nil || e.Recovery == nil || e.Publisher == nil || e.Activation == nil {
		return errors.New("realm removal executor is incomplete")
	}
	if record.State != repoworker.RealmRemovalCompleted {
		if err := e.Activation.FenceRealmRemoval(record.RealmID, record.OperationID, record.Scope.ClientIDs); err != nil {
			return err
		}
	}
	if record.Request.ErasureRequested {
		if e.Erasure == nil {
			return errors.New("data erasure journal is unavailable")
		}
		if _, err := e.Erasure.Accept(record, e.ErasureMaxDays); err != nil {
			return err
		}
	}
	if record.State == repoworker.RealmRemovalDeleting {
		for _, repoID := range record.Scope.OwnedRepoIDs {
			op := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.OperationID+":"+repoID+":delete")).String()
			if _, err := e.Backend.Delete(ctx, op, record.RealmID, repoID); err != nil {
				return err
			}
		}
		if err := e.Recovery.Prepare(record); err != nil {
			return err
		}
		if _, err := e.Store.Advance(record.OperationID, repoworker.RealmRemovalDeleting, repoworker.RealmRemovalRecoveryReady); err != nil {
			return err
		}
		record.State = repoworker.RealmRemovalRecoveryReady
	}
	if record.State == repoworker.RealmRemovalRecoveryReady {
		if err := e.Publisher.WithdrawRealmGrants(ctx, record.RealmID, record.Scope.ForeignGrantRepoIDs); err != nil {
			return err
		}
		if _, err := e.Store.Advance(record.OperationID, repoworker.RealmRemovalRecoveryReady, repoworker.RealmRemovalRevokingClients); err != nil {
			return err
		}
		record.State = repoworker.RealmRemovalRevokingClients
	}
	if record.State == repoworker.RealmRemovalRevokingClients {
		if _, err := e.Activation.RevokeRealmRemoval(ctx, record.RealmID, record.OperationID, record.Scope.ClientIDs, "realm removal confirmed"); err != nil {
			return err
		}
		if record.Request.ErasureRequested {
			if _, err := e.Erasure.MarkActiveDataDeleted(record.OperationID); err != nil {
				return err
			}
		}
		_, err := e.Store.Advance(record.OperationID, repoworker.RealmRemovalRevokingClients, repoworker.RealmRemovalCompleted)
		return err
	}
	return nil
}
