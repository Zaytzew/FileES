package repoworker

import (
	"context"
	"errors"

	control "filees/pkg/control/v1"
)

func (w *Worker) publicShare(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.PublicShares == nil {
		return w.failure(ticket, "PUBLIC_SHARE_UNAVAILABLE", "public shares are unavailable")
	}
	var result control.PublicShareResult
	var err error
	switch ticket.Type {
	case control.TicketCreatePublicShare:
		var payload control.CreatePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		result, err = w.PublicShares.Create(ctx, ticket.OperationID, session.RealmID, payload.PublicShareDeclaration)
	case control.TicketUpdatePublicShare:
		var payload control.UpdatePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		result, err = w.PublicShares.Update(ctx, ticket.OperationID, session.RealmID, payload.ChannelID, payload.PublicShareDeclaration)
	case control.TicketRevokePublicShare:
		var payload control.RevokePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		result, err = w.PublicShares.Revoke(ctx, session.RealmID, payload.ChannelID)
	case control.TicketDeletePublicShare:
		var payload control.DeletePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		result, err = w.PublicShares.Delete(ctx, session.RealmID, payload.ChannelID)
	default:
		return control.Result{}, errors.New("unsupported public share ticket")
	}
	if err != nil {
		if errors.Is(err, ErrPublicShareRejected) {
			return w.failure(ticket, "PUBLIC_SHARE_REJECTED", "public share request was rejected")
		}
		return w.retryable(ticket, "PUBLIC_SHARE_RETRY", err.Error())
	}
	wire, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, result, w.now())
	if err == nil {
		err = w.Store.Save(wire)
	}
	return wire, err
}
