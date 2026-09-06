package servertool

import (
	"context"

	"filees/pkg/repoworker"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationprojection"
	"filees/pkg/serverconfig"
)

// Caller holds the authority/service-WC lock. This function neither reconciles
// nor rewrites authority: only the disposable history/lock artifacts may change.
func refreshAutolockProjection(ctx context.Context, config serverconfig.Config, clientID string, req reservationv1.Request, stateRoot string) (reservationv1.Result, error) {
	lifecycle := req
	lifecycle.Schema = reservationv1.StateSchema
	view, deleted, err := authorizedStateView(config.Activation.ServiceWorkingCopy, clientID, lifecycle)
	if err != nil {
		return reservationv1.Result{}, err
	}
	r := reservationv1.Result{Schema: reservationv1.AutolockSchema, RepoID: req.RepoID, Reservations: []reservationv1.Reservation{}, RepositoryState: "deleted"}
	if !deleted {
		authority := repoworker.PassportReplacementAuthority{ServiceWC: config.Activation.ServiceWorkingCopy}
		session := repoworker.Session{ClientID: clientID, RealmID: view.RealmID, Repositories: view.Repositories}
		if err := authority.AuthorizeOwnershipRead(session, req.RepoID); err != nil {
			return r, err
		}
		r = refreshReservationProjection(ctx, reservationprojection.NewStore(stateRoot), config.Activation.SVNBinary, config.Repositories.Root, req.RepoID)
		r.Schema, r.RepositoryState = reservationv1.AutolockSchema, "active"
		if r.Stale || r.Unknown {
			r.OwnershipDetail = "live reservations unavailable"
		} else {
			source := repoworker.SVNPathOwners{SVN: config.Activation.SVNBinary, RepositoriesRoot: config.Repositories.Root, ServiceWC: config.Activation.ServiceWorkingCopy, CacheRoot: stateRoot}
			snapshot, err := source.Snapshot(ctx, req.RepoID)
			if err != nil {
				r.OwnershipDetail = err.Error()
			} else {
				r.PathOwnership = &snapshot
			}
			for i := range r.Reservations {
				r.Reservations[i].OwnerRealmID = authority.LockOwnerRealm(r.Reservations[i].OwnerID)
			}
		}
	}
	r.ViewGeneration = view.Generation
	produced := view.GeneratedAt
	r.ViewGeneratedAt = &produced
	return r, nil
}
