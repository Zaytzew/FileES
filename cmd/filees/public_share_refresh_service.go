package main

import (
	"context"

	contract "filees/pkg/contract/v1"
)

// refreshingPublicShareService decorates the server-backed service with the
// local cache invalidation E1 needs. The remote mutation remains authoritative;
// a notification is emitted only after it has succeeded.
type refreshingPublicShareService struct {
	delegate publicShareService
	changed  func(serverID string)
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
	result, err := service.delegate.CreatePublicShare(ctx, serverID, declaration)
	service.notify(serverID, err)
	return result, err
}

func (service refreshingPublicShareService) UpdatePublicShare(ctx context.Context, serverID, channelID string, declaration contract.PublicShareDeclaration, keepPassword bool) (contract.PublicShareResult, error) {
	result, err := service.delegate.UpdatePublicShare(ctx, serverID, channelID, declaration, keepPassword)
	service.notify(serverID, err)
	return result, err
}

func (service refreshingPublicShareService) RevokePublicShare(ctx context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	result, err := service.delegate.RevokePublicShare(ctx, serverID, channelID)
	service.notify(serverID, err)
	return result, err
}

func (service refreshingPublicShareService) DeletePublicShare(ctx context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	result, err := service.delegate.DeletePublicShare(ctx, serverID, channelID)
	service.notify(serverID, err)
	return result, err
}

func (service refreshingPublicShareService) notify(serverID string, err error) {
	if err == nil && service.changed != nil {
		service.changed(serverID)
	}
}
