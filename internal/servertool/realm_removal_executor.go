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
	Store      repoworker.RealmRemovalStore
	Backend    realmRemovalBackend
	Publisher  realmRemovalGrantPublisher
	Activation realmRemovalRevoker
}

type realmRemovalBackend interface {
	Delete(context.Context, string, string, string) (time.Time, error)
}
type realmRemovalGrantPublisher interface {
	WithdrawRealmGrants(context.Context, string, []string) error
}
type realmRemovalRevoker interface {
	RevokeRealm(context.Context, string, string) ([]string, error)
}

func (e realmRemovalExecutor) Execute(ctx context.Context, record repoworker.RealmRemovalRecord) error {
	if e.Backend == nil || e.Activation == nil {
		return errors.New("realm removal executor is incomplete")
	}
	if record.State == repoworker.RealmRemovalDeleting {
		for _, repoID := range record.Scope.OwnedRepoIDs {
			op := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.OperationID+":"+repoID+":delete")).String()
			if _, err := e.Backend.Delete(ctx, op, record.RealmID, repoID); err != nil {
				return err
			}
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
		if _, err := e.Activation.RevokeRealm(ctx, record.RealmID, "realm removal confirmed"); err != nil {
			return err
		}
		_, err := e.Store.Advance(record.OperationID, repoworker.RealmRemovalRevokingClients, repoworker.RealmRemovalCompleted)
		return err
	}
	return nil
}
