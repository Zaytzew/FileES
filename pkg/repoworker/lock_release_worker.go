package repoworker

import (
	"context"
	"errors"
	"time"

	control "filees/pkg/control/v1"
)

func (w *Worker) lockRelease(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.LockReleases == nil || w.LockAuthority == nil || w.LockProjector == nil {
		return w.retryable(ticket, "LOCK_RELEASE_UNAVAILABLE", "lock release requests are unavailable")
	}
	if ticket.Type == control.TicketRequestLockRelease {
		return w.requestLockRelease(ctx, session, ticket)
	}
	return w.decideLockRelease(ctx, session, ticket)
}

func (w *Worker) requestLockRelease(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	var payload control.RequestLockReleasePayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	if !sessionCanWriteRepository(session, payload.RepoID) {
		return w.retryable(ticket, "LOCK_RELEASE_FORBIDDEN", "authenticated session cannot request this lock")
	}
	observation, err := w.LockAuthority.InspectLock(ctx, payload.RepoID, payload.Path)
	if err != nil {
		return w.retryable(ticket, "LOCK_RELEASE_INSPECTION_FAILED", "the authoritative lock could not be inspected")
	}
	if observation == nil || observation.ObservedLockID != payload.ObservedLockID {
		return w.retryable(ticket, "LOCK_RELEASE_STALE", "the observed lock no longer exists")
	}
	if observation.HolderClientID == session.ClientID {
		return w.retryable(ticket, "LOCK_RELEASE_SELF", "the authenticated installation already holds this lock")
	}
	record, _, err := w.LockReleases.Request(LockReleaseRequest{
		RepoID: payload.RepoID, Path: payload.Path, ObservedLockID: payload.ObservedLockID,
		RequesterClientID: session.ClientID, RequesterRealmID: session.RealmID,
		HolderClientID: observation.HolderClientID, HolderRealmID: observation.HolderRealmID,
	})
	if err != nil {
		return w.retryable(ticket, "LOCK_RELEASE_REJECTED", err.Error())
	}
	if err := w.LockProjector.PublishLockRelease(ctx, record); err != nil {
		return w.retryable(ticket, "LOCK_RELEASE_PROJECTION_FAILED", "lock release request could not be projected")
	}
	return w.lockReleaseSuccess(ticket, record)
}

func (w *Worker) decideLockRelease(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	var payload control.DecideLockReleasePayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	record, err := w.LockReleases.Get(payload.RequestID)
	if errors.Is(err, ErrLockReleaseNotFound) {
		return w.retryable(ticket, "LOCK_RELEASE_NOT_FOUND", "lock release request no longer exists")
	}
	if err != nil {
		return w.retryable(ticket, "LOCK_RELEASE_UNAVAILABLE", err.Error())
	}
	if !sessionCanWriteRepository(session, record.RepoID) {
		return w.retryable(ticket, "LOCK_RELEASE_FORBIDDEN", "authenticated session cannot answer this request")
	}
	observation, err := w.LockAuthority.InspectLock(ctx, record.RepoID, record.Path)
	if err != nil {
		return w.retryable(ticket, "LOCK_RELEASE_INSPECTION_FAILED", "the authoritative lock could not be inspected")
	}
	record, err = w.LockReleases.Reconcile(record.RequestID, observation)
	if err != nil && !errors.Is(err, ErrLockReleaseTerminal) {
		return w.retryable(ticket, "LOCK_RELEASE_REJECTED", err.Error())
	}
	if record.State != LockReleasePending {
		if err := w.LockProjector.PublishLockRelease(ctx, record); err != nil {
			return w.retryable(ticket, "LOCK_RELEASE_PROJECTION_FAILED", "lock release state could not be projected")
		}
		return w.lockReleaseSuccess(ticket, record)
	}
	next := LockReleaseDismissed
	if ticket.Type == control.TicketAcceptLockRelease {
		next = LockReleaseAccepted
	}
	record, err = w.LockReleases.Respond(record.RequestID, session.ClientID, next)
	if errors.Is(err, ErrLockReleaseForbidden) {
		return w.retryable(ticket, "LOCK_RELEASE_FORBIDDEN", "only the current lock holder can answer this request")
	}
	if errors.Is(err, ErrLockReleaseTerminal) {
		return w.lockReleaseSuccess(ticket, record)
	}
	if err != nil {
		return w.retryable(ticket, "LOCK_RELEASE_REJECTED", err.Error())
	}
	if err := w.LockProjector.PublishLockRelease(ctx, record); err != nil {
		return w.retryable(ticket, "LOCK_RELEASE_PROJECTION_FAILED", "lock release response could not be projected")
	}
	return w.lockReleaseSuccess(ticket, record)
}

func (w *Worker) lockReleaseSuccess(ticket control.Ticket, record LockReleaseRecord) (control.Result, error) {
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.LockReleaseResult{
		RequestID: record.RequestID, RepoID: record.RepoID, Path: record.Path,
		ObservedLockID: record.ObservedLockID, State: string(record.State),
		ExpiresAt: record.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, w.now())
}

func sessionCanWriteRepository(session Session, repoID string) bool {
	for _, repo := range session.Repositories {
		if repo.RepoID == repoID && repo.State == "active" && repo.Access == "rw" {
			return true
		}
	}
	return false
}
