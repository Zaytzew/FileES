package ipcserver

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
)

type uploadChannelStub struct {
	action, serverID, repoID, channelID string
	declaration                         contract.UploadChannelDeclaration
}

func (s *uploadChannelStub) ListUploadChannels(_ context.Context, serverID, repoID string) ([]contract.UploadChannelSummary, error) {
	s.action, s.serverID, s.repoID = "list", serverID, repoID
	return []contract.UploadChannelSummary{{ChannelID: "channel-1", AuthorityRepoID: repoID, UploadRepoID: "upload-1", Alias: "acme", Slug: "oferta-a", State: "active", Recipients: []string{"a@example.com"}}}, nil
}
func (s *uploadChannelStub) CreateUploadChannel(_ context.Context, serverID string, declaration contract.UploadChannelDeclaration) (contract.UploadChannelResult, error) {
	s.action, s.serverID, s.repoID, s.declaration = "create", serverID, declaration.AuthorityRepoID, declaration
	return contract.UploadChannelResult{ChannelID: "channel-1", Alias: "acme", Slug: declaration.Slug, State: "active", UploadRepoID: "upload-1"}, nil
}
func (s *uploadChannelStub) UpdateUploadChannel(_ context.Context, serverID, channelID string, declaration contract.UploadChannelDeclaration) (contract.UploadChannelResult, error) {
	s.action, s.serverID, s.repoID, s.channelID, s.declaration = "update", serverID, declaration.AuthorityRepoID, channelID, declaration
	return contract.UploadChannelResult{ChannelID: channelID, Alias: "acme", Slug: declaration.Slug, State: "active", UploadRepoID: "upload-1"}, nil
}
func (s *uploadChannelStub) RevokeUploadChannel(_ context.Context, serverID, channelID string) (contract.UploadChannelResult, error) {
	s.action, s.serverID, s.channelID = "revoke", serverID, channelID
	return contract.UploadChannelResult{ChannelID: channelID, State: "revoked"}, nil
}
func (s *uploadChannelStub) DeleteUploadChannel(_ context.Context, serverID, channelID string) (contract.UploadChannelResult, error) {
	s.action, s.serverID, s.channelID = "delete", serverID, channelID
	return contract.UploadChannelResult{ChannelID: channelID, State: "deleted"}, nil
}

func TestUploadChannelCapabilitiesAreAdvertisedOnlyWhenWired(t *testing.T) {
	server := New("unused")
	capabilities := []string{contract.CapRepoUploadChannelList, contract.CapRepoUploadChannelCreate, contract.CapRepoUploadChannelUpdate, contract.CapRepoUploadChannelRevoke, contract.CapRepoUploadChannelDelete}
	for _, capability := range capabilities {
		if containsCapability(server.capabilities(), capability) {
			t.Fatalf("unwired capability advertised: %s", capability)
		}
	}
	server.SetUploadChannelService(&uploadChannelStub{})
	for _, capability := range capabilities {
		if !containsCapability(server.capabilities(), capability) {
			t.Fatalf("wired capability missing: %s", capability)
		}
	}
}

func TestUploadChannelIPCListsAndMutatesOwnedRepository(t *testing.T) {
	stub := &uploadChannelStub{}
	server := New("unused")
	server.SetUploadChannelService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)

	response := server.dispatch(lifecycleRequest(contract.CmdRepoUploadChannelList, contract.UploadChannelListPayload{ServerID: "office", RepoID: "repo-1"}))
	var list contract.UploadChannelListResult
	if response.Status != contract.StatusOK || contract.DecodeResult(response.Result, &list) != nil || len(list.Channels) != 1 || stub.action != "list" || stub.repoID != "repo-1" {
		t.Fatalf("list response=%+v result=%+v stub=%+v", response, list, stub)
	}

	declaration := contract.UploadChannelDeclaration{AuthorityRepoID: "repo-1", Slug: "oferta-a", Recipients: []string{"a@example.com"}}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoUploadChannelCreate, contract.UploadChannelCreatePayload{ServerID: "office", UploadChannelDeclaration: declaration}))
	if response.Status != contract.StatusOK || stub.action != "create" {
		t.Fatalf("create response=%+v stub=%+v", response, stub)
	}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoUploadChannelUpdate, contract.UploadChannelUpdatePayload{ServerID: "office", ChannelID: "channel-1", UploadChannelDeclaration: declaration}))
	if response.Status != contract.StatusOK || stub.action != "update" {
		t.Fatalf("update response=%+v stub=%+v", response, stub)
	}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoUploadChannelRevoke, contract.UploadChannelChannelPayload{ServerID: "office", RepoID: "repo-1", ChannelID: "channel-1"}))
	if response.Status != contract.StatusOK || stub.action != "revoke" {
		t.Fatalf("revoke response=%+v stub=%+v", response, stub)
	}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoUploadChannelDelete, contract.UploadChannelChannelPayload{ServerID: "office", RepoID: "repo-1", ChannelID: "channel-1"}))
	if response.Status != contract.StatusOK || stub.action != "delete" {
		t.Fatalf("delete response=%+v stub=%+v", response, stub)
	}
}

func TestUploadChannelIPCRejectsForeignRepositoryBeforeService(t *testing.T) {
	stub := &uploadChannelStub{}
	server := New("unused")
	server.SetUploadChannelService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "caller", CanCreateRepositories: true})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoUploadChannelList, contract.UploadChannelListPayload{ServerID: "office", RepoID: "repo-1"}))
	if response.Status == contract.StatusOK || stub.action != "" {
		t.Fatalf("foreign repository reached service: response=%+v stub=%+v", response, stub)
	}
}
