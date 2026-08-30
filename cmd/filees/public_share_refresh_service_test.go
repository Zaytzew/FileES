package main

import (
	"context"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
)

type recordingPublicShareService struct {
	err error
}

func (service *recordingPublicShareService) ListPublicShares(context.Context, string, string) ([]contract.PublicShareSummary, error) {
	return nil, service.err
}

func (service *recordingPublicShareService) CreatePublicShare(context.Context, string, contract.PublicShareDeclaration) (contract.PublicShareResult, error) {
	return contract.PublicShareResult{ChannelID: "created"}, service.err
}

func (service *recordingPublicShareService) UpdatePublicShare(context.Context, string, string, contract.PublicShareDeclaration, bool) (contract.PublicShareResult, error) {
	return contract.PublicShareResult{ChannelID: "updated"}, service.err
}

func (service *recordingPublicShareService) RevokePublicShare(context.Context, string, string) (contract.PublicShareResult, error) {
	return contract.PublicShareResult{ChannelID: "revoked"}, service.err
}

func (service *recordingPublicShareService) DeletePublicShare(context.Context, string, string) (contract.PublicShareResult, error) {
	return contract.PublicShareResult{ChannelID: "deleted"}, service.err
}

func TestRefreshingPublicShareServiceNotifiesOnlySuccessfulMutations(t *testing.T) {
	delegate := &recordingPublicShareService{}
	var changed []string
	service := refreshingPublicShareService{delegate: delegate, changed: func(serverID string) { changed = append(changed, serverID) }}

	if _, err := service.ListPublicShares(t.Context(), "spot", "repo"); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("read notified a mutation: %v", changed)
	}
	if _, err := service.CreatePublicShare(t.Context(), "spot", contract.PublicShareDeclaration{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePublicShare(t.Context(), "spot", "channel", contract.PublicShareDeclaration{}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokePublicShare(t.Context(), "spot", "channel"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeletePublicShare(t.Context(), "spot", "channel"); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 4 {
		t.Fatalf("successful mutation notifications = %v", changed)
	}

	delegate.err = errors.New("remote mutation failed")
	if _, err := service.RevokePublicShare(t.Context(), "spot", "channel"); err == nil {
		t.Fatal("failed mutation returned success")
	}
	if len(changed) != 4 {
		t.Fatalf("failed mutation notified cache refresh: %v", changed)
	}
}
