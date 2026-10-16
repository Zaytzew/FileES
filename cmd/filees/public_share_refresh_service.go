package main

import (
	"context"

	contract "filees/pkg/contract/v1"
)

// refreshingPublicShareService decorates the server-backed service with the
// local notification E1 needs. A canonical ACK updates the cache directly;
// only a legacy/inapplicable receipt schedules a read. Failed mutations never
// emit success, and a local cache is never mutation authority.
type refreshingPublicShareService struct {
	delegate publicShareService
	changed  func(serverID string)
	observed func(serverID string)
}

type publicShareService interface {
	ListPublicShares(context.Context, string, string) ([]contract.PublicShareSummary, error)
	CreatePublicShare(context.Context, string, contract.PublicShareDeclaration) (contract.PublicShareResult, error)
	UpdatePublicShare(context.Context, string, string, contract.PublicShareDeclaration, bool) (contract.PublicShareResult, error)
	RevokePublicShare(context.Context, string, string) (contract.PublicShareResult, error)
	DeletePublicShare(context.Context, string, string) (contract.PublicShareResult, error)
}

func (service refreshingPublicShareService) ListPublicShares(ctx context.Context, serverID, repoID string) ([]contract.PublicShareSummary, error) {
	return service.delegate.ListPublicShares(ctx, serverID, repoID)
}

func (service refreshingPublicShareService) CreatePublicShare(ctx context.Context, serverID string, declaration contract.PublicShareDeclaration) (contract.PublicShareResult, error) {
	ctx = withPublicShareReceipt(ctx)
	result, err := service.delegate.CreatePublicShare(ctx, serverID, declaration)
	service.notify(ctx, serverID, err)
	return result, err
}

func (service refreshingPublicShareService) UpdatePublicShare(ctx context.Context, serverID, channelID string, declaration contract.PublicShareDeclaration, keepPassword bool) (contract.PublicShareResult, error) {
	ctx = withPublicShareReceipt(ctx)
	result, err := service.delegate.UpdatePublicShare(ctx, serverID, channelID, declaration, keepPassword)
	service.notify(ctx, serverID, err)
	return result, err
}

func (service refreshingPublicShareService) RevokePublicShare(ctx context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	ctx = withPublicShareReceipt(ctx)
	result, err := service.delegate.RevokePublicShare(ctx, serverID, channelID)
	service.notify(ctx, serverID, err)
	return result, err
}

func (service refreshingPublicShareService) DeletePublicShare(ctx context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	ctx = withPublicShareReceipt(ctx)
	result, err := service.delegate.DeletePublicShare(ctx, serverID, channelID)
	service.notify(ctx, serverID, err)
	return result, err
}

func (service refreshingPublicShareService) notify(ctx context.Context, serverID string, err error) {
	if applied, ok := ctx.Value(publicShareReceiptKey{}).(*bool); ok && *applied && err == nil {
		if service.observed != nil {
			service.observed(serverID)
		}
		return
	}
	if err == nil && service.changed != nil {
		service.changed(serverID)
	}
}
