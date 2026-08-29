package main

import (
	"context"
	"testing"

	"filees/internal/gui/actions"
	contract "filees/pkg/contract/v1"
	"github.com/google/uuid"
)

type uploadChannelClientStub struct {
	last contract.UploadChannelCreatePayload
}

func (s *uploadChannelClientStub) UploadChannelList(context.Context, string, string) (*contract.UploadChannelListResult, error) {
	return &contract.UploadChannelListResult{}, nil
}
func (s *uploadChannelClientStub) UploadChannelCreate(_ context.Context, payload contract.UploadChannelCreatePayload) (*contract.UploadChannelResult, error) {
	s.last = payload
	return &contract.UploadChannelResult{ChannelID: uuid.NewString(), Alias: "acme", Slug: payload.Slug, State: "active"}, nil
}
func (uploadChannelClientStub) UploadChannelUpdate(context.Context, contract.UploadChannelUpdatePayload) (*contract.UploadChannelResult, error) {
	return &contract.UploadChannelResult{ChannelID: "channel-1", State: "active"}, nil
}
func (uploadChannelClientStub) UploadChannelRevoke(context.Context, contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error) {
	return &contract.UploadChannelResult{ChannelID: "channel-1", State: "revoked"}, nil
}
func (uploadChannelClientStub) UploadChannelDelete(context.Context, contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error) {
	return &contract.UploadChannelResult{ChannelID: "channel-1", State: "deleted"}, nil
}

func TestUploadChannelAdapterCreateIsShelfWithoutOTP(t *testing.T) {
	stub := &uploadChannelClientStub{}
	adapter := uploadChannelAdapter{client: stub}
	repoID := uuid.NewString()
	if err := adapter.CreateUploadChannel(context.Background(), "office", actions.UploadChannelDeclaration{AuthorityRepoID: repoID, Slug: "oferta-a", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if stub.last.AuthorityRepoID != repoID || stub.last.Slug != "oferta-a" || len(stub.last.Recipients) != 1 || stub.last.RequireOTP {
		t.Fatalf("payload=%+v", stub.last)
	}
}

func TestUploadChannelAdapterCreatePassesRequireOTP(t *testing.T) {
	stub := &uploadChannelClientStub{}
	adapter := uploadChannelAdapter{client: stub}
	repoID := uuid.NewString()
	if err := adapter.CreateUploadChannel(context.Background(), "office", actions.UploadChannelDeclaration{AuthorityRepoID: repoID, Slug: "oferta-a", Recipients: []string{"a@example.com"}, RequireOTP: true}); err != nil {
		t.Fatal(err)
	}
	if !stub.last.RequireOTP {
		t.Fatalf("payload=%+v", stub.last)
	}
}
