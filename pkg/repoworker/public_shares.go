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
	var response any
	var err error
	includeState := false
	switch ticket.Type {
	case control.TicketListPublicShares:
		var payload control.ListPublicSharesPayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		var shares []control.PublicShareSummary
		shares, err = w.PublicShares.List(ctx, session.RealmID, payload.RepoID)
		canManage := false
		if capable, ok := w.PublicShares.(interface{ CanManageInvitations() bool }); ok {
			canManage = capable.CanManageInvitations()
		}
		response = publicShareListForClient(shares, canManage, payload.IncludeInvitations)
	case control.TicketCreatePublicShare:
		var payload control.CreatePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.PublicShares.Create(ctx, ticket.OperationID, session.RealmID, payload.PublicShareDeclaration)
		includeState = payload.IncludeState
	case control.TicketUpdatePublicShare:
		var payload control.UpdatePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.PublicShares.Update(ctx, ticket.OperationID, session.RealmID, payload.ChannelID, payload.PublicShareDeclaration, payload.KeepPassword)
		includeState = payload.IncludeState
	case control.TicketRevokePublicShare:
		var payload control.RevokePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.PublicShares.Revoke(ctx, session.RealmID, payload.ChannelID)
		includeState = payload.IncludeState
	case control.TicketDeletePublicShare:
		var payload control.DeletePublicSharePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.PublicShares.Delete(ctx, session.RealmID, payload.ChannelID)
		includeState = payload.IncludeState
	default:
		return control.Result{}, errors.New("unsupported public share ticket")
	}
	if err != nil {
		if errors.Is(err, ErrPublicShareRejected) {
			return w.failure(ticket, "PUBLIC_SHARE_REJECTED", "public share request was rejected")
		}
		return w.retryable(ticket, "PUBLIC_SHARE_RETRY", err.Error())
	}
	if result, ok := response.(control.PublicShareResult); ok {
		if !includeState {
			result.StateReceipt = nil // old clients strictly decode mutation results
		} else if result.StateReceipt != nil {
			for _, repo := range session.Repositories {
				if repo.RepoID == result.StateReceipt.RepoID {
					result.StateReceipt.RepoDisplayName = repo.DisplayName
					break
				}
			}
		}
		response = result
	}
	wire, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, response, w.now())
	if err == nil {
		err = w.Store.Save(wire)
	}
	return wire, err
}

func publicShareListForClient(shares []control.PublicShareSummary, capable, include bool) control.ListPublicSharesResult {
	copy := append([]control.PublicShareSummary(nil), shares...)
	if !include {
		for i := range copy {
			copy[i].InvitationMessage = ""
		}
	}
	return control.ListPublicSharesResult{Shares: copy, CanManageInvitations: capable && include}
}
