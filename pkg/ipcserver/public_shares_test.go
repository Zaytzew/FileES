package ipcserver

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
)

type publicShareStub struct {
	action, serverID, repoID, channelID string
	declaration                         contract.PublicShareDeclaration
	keepPassword                        bool
}

func (s *publicShareStub) ListPublicShares(_ context.Context, serverID, repoID string) ([]contract.PublicShareSummary, error) {
	s.action, s.serverID, s.repoID = "list", serverID, repoID
	return []contract.PublicShareSummary{{ChannelID: "channel-1", RepoID: repoID, Alias: "acme", Slug: "wydanie", State: "active", SourceRoot: "public", Objects: []contract.PublicShareObject{{PublicID: "1234567890abcdef", RepoPath: "public/a.txt", DisplayName: "a.txt"}}}}, nil
}
func (s *publicShareStub) CreatePublicShare(_ context.Context, serverID string, declaration contract.PublicShareDeclaration) (contract.PublicShareResult, error) {
	s.action, s.serverID, s.repoID, s.declaration = "create", serverID, declaration.RepoID, declaration
	return contract.PublicShareResult{ChannelID: "channel-1", Alias: "acme", Slug: declaration.Slug, State: "active"}, nil
}
func (s *publicShareStub) UpdatePublicShare(_ context.Context, serverID, channelID string, declaration contract.PublicShareDeclaration, keepPassword bool) (contract.PublicShareResult, error) {
	s.action, s.serverID, s.repoID, s.channelID, s.declaration, s.keepPassword = "update", serverID, declaration.RepoID, channelID, declaration, keepPassword
	return contract.PublicShareResult{ChannelID: channelID, Alias: "acme", Slug: declaration.Slug, State: "active"}, nil
}
func (s *publicShareStub) RevokePublicShare(_ context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	s.action, s.serverID, s.channelID = "revoke", serverID, channelID
	return contract.PublicShareResult{ChannelID: channelID, State: "revoked"}, nil
}
func (s *publicShareStub) DeletePublicShare(_ context.Context, serverID, channelID string) (contract.PublicShareResult, error) {
	s.action, s.serverID, s.channelID = "delete", serverID, channelID
	return contract.PublicShareResult{ChannelID: channelID, State: "deleted"}, nil
}

func TestPublicShareCapabilitiesAreAdvertisedOnlyWhenWired(t *testing.T) {
	server := New("unused")
	capabilities := []string{contract.CapRepoPublicShareList, contract.CapRepoPublicShareCreate, contract.CapRepoPublicShareUpdate, contract.CapRepoPublicShareRevoke, contract.CapRepoPublicShareDelete}
	for _, capability := range capabilities {
		if containsCapability(server.capabilities(), capability) {
			t.Fatalf("unwired capability advertised: %s", capability)
		}
	}
	server.SetPublicShareService(&publicShareStub{})
	for _, capability := range capabilities {
		if !containsCapability(server.capabilities(), capability) {
			t.Fatalf("wired capability missing: %s", capability)
		}
	}
}

func TestPublicShareIPCListsAndMutatesOwnedRepository(t *testing.T) {
	stub := &publicShareStub{}
	server := New("unused")
	server.SetPublicShareService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)

	response := server.dispatch(lifecycleRequest(contract.CmdRepoPublicShareList, contract.PublicShareListPayload{ServerID: "office", RepoID: "repo-1"}))
	var list contract.PublicShareListResult
	if response.Status != contract.StatusOK || contract.DecodeResult(response.Result, &list) != nil || len(list.Shares) != 1 || stub.action != "list" || stub.repoID != "repo-1" {
		t.Fatalf("list response=%+v result=%+v stub=%+v", response, list, stub)
	}

	declaration := contract.PublicShareDeclaration{RepoID: "repo-1", SourceRoot: "public", Slug: "wydanie", Objects: []contract.PublicShareObject{{PublicID: "1234567890abcdef", RepoPath: "public/a.txt", DisplayName: "a.txt"}}}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoPublicShareCreate, contract.PublicShareCreatePayload{ServerID: "office", PublicShareDeclaration: declaration}))
	if response.Status != contract.StatusOK || stub.action != "create" {
		t.Fatalf("create response=%+v stub=%+v", response, stub)
	}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoPublicShareUpdate, contract.PublicShareUpdatePayload{ServerID: "office", ChannelID: "channel-1", KeepPassword: true, PublicShareDeclaration: declaration}))
	if response.Status != contract.StatusOK || stub.action != "update" || !stub.keepPassword {
		t.Fatalf("update response=%+v stub=%+v", response, stub)
	}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoPublicShareRevoke, contract.PublicShareChannelPayload{ServerID: "office", RepoID: "repo-1", ChannelID: "channel-1"}))
	if response.Status != contract.StatusOK || stub.action != "revoke" {
		t.Fatalf("revoke response=%+v stub=%+v", response, stub)
	}
	response = server.dispatch(lifecycleRequest(contract.CmdRepoPublicShareDelete, contract.PublicShareChannelPayload{ServerID: "office", RepoID: "repo-1", ChannelID: "channel-1"}))
	if response.Status != contract.StatusOK || stub.action != "delete" {
		t.Fatalf("delete response=%+v stub=%+v", response, stub)
	}
}

func TestPublicShareIPCRejectsForeignRepositoryBeforeService(t *testing.T) {
	stub := &publicShareStub{}
	server := New("unused")
	server.SetPublicShareService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "caller", CanCreateRepositories: true})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoPublicShareList, contract.PublicShareListPayload{ServerID: "office", RepoID: "repo-1"}))
	if response.Status == contract.StatusOK || stub.action != "" {
		t.Fatalf("foreign repository reached service: response=%+v stub=%+v", response, stub)
	}
}
