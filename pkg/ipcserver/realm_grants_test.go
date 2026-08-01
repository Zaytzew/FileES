package ipcserver

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
)

type realmGrantStub struct {
	recipients []contract.RealmGrantRecipient
	serverID   string
	repoID     string
	realmID    string
	access     string
	revoked    bool
	visibility string
}

func (stub *realmGrantStub) ListRecipients(_ context.Context, serverID string) ([]contract.RealmGrantRecipient, error) {
	stub.serverID = serverID
	return stub.recipients, nil
}

func (stub *realmGrantStub) SetVisibility(_ context.Context, serverID, visibility string) (string, error) {
	stub.serverID, stub.visibility = serverID, visibility
	return visibility, nil
}

func (stub *realmGrantStub) Grant(_ context.Context, serverID, repoID, realmID, access string) (contract.RealmGrantResult, error) {
	stub.serverID, stub.repoID, stub.realmID, stub.access = serverID, repoID, realmID, access
	return contract.RealmGrantResult{RepoID: repoID, RecipientRealmID: realmID, Access: access, State: "active"}, nil
}

func (stub *realmGrantStub) Revoke(_ context.Context, serverID, repoID, realmID string) (contract.RealmGrantResult, error) {
	stub.serverID, stub.repoID, stub.realmID, stub.revoked = serverID, repoID, realmID, true
	return contract.RealmGrantResult{RepoID: repoID, RecipientRealmID: realmID, State: "revoked"}, nil
}

func TestRealmGrantCapabilitiesAreAdvertisedOnlyWhenWired(t *testing.T) {
	server := New("unused")
	for _, capability := range []string{contract.CapRealmGrantRecipients, contract.CapRealmSetVisibility, contract.CapRepoGrantAccess, contract.CapRepoRevokeAccess} {
		if containsCapability(server.capabilities(), capability) {
			t.Fatalf("unwired capability advertised: %s", capability)
		}
	}
	server.SetRealmGrantService(&realmGrantStub{})
	for _, capability := range []string{contract.CapRealmGrantRecipients, contract.CapRealmSetVisibility, contract.CapRepoGrantAccess, contract.CapRepoRevokeAccess} {
		if !containsCapability(server.capabilities(), capability) {
			t.Fatalf("wired capability missing: %s", capability)
		}
	}
}

func TestRealmGrantIPCListsAndMutatesOwnedRepository(t *testing.T) {
	const owner = "owner-realm"
	stub := &realmGrantStub{recipients: []contract.RealmGrantRecipient{{RealmID: "recipient-realm", Alias: "biuro"}}}
	server := New("unused")
	server.SetRealmGrantService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: owner, CanCreateRepositories: true})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", owner, "optional", true)

	list := lifecycleRequest(contract.CmdRealmGrantRecipients, contract.RealmGrantRecipientsPayload{ServerID: "office"})
	response := server.dispatch(list)
	if response.Status != contract.StatusOK {
		t.Fatalf("recipient list rejected: %+v", response.Error)
	}
	var listed contract.RealmGrantRecipientsResult
	if err := contract.DecodeResult(response.Result, &listed); err != nil || len(listed.Recipients) != 1 || listed.Recipients[0].Alias != "biuro" {
		t.Fatalf("recipient list=%+v err=%v", listed, err)
	}
	visibility := lifecycleRequest(contract.CmdRealmSetVisibility, contract.RealmSetVisibilityPayload{ServerID: "office", Visibility: "listed"})
	if response := server.dispatch(visibility); response.Status != contract.StatusOK {
		t.Fatalf("visibility rejected: %+v", response.Error)
	}
	if stub.visibility != "listed" {
		t.Fatalf("visibility call=%+v", stub)
	}

	grant := lifecycleRequest(contract.CmdRepoGrantAccess, contract.RepoGrantAccessPayload{ServerID: "office", RepoID: "repo-1", RecipientRealmID: "recipient-realm", Access: "rw"})
	if response := server.dispatch(grant); response.Status != contract.StatusOK {
		t.Fatalf("grant rejected: %+v", response.Error)
	}
	if stub.serverID != "office" || stub.repoID != "repo-1" || stub.realmID != "recipient-realm" || stub.access != "rw" {
		t.Fatalf("grant call=%+v", stub)
	}

	revoke := lifecycleRequest(contract.CmdRepoRevokeAccess, contract.RepoRevokeAccessPayload{ServerID: "office", RepoID: "repo-1", RecipientRealmID: "recipient-realm"})
	if response := server.dispatch(revoke); response.Status != contract.StatusOK {
		t.Fatalf("revoke rejected: %+v", response.Error)
	}
	if !stub.revoked {
		t.Fatal("revoke did not reach service")
	}
}

func TestRealmGrantIPCRejectsForeignRepositoryBeforeService(t *testing.T) {
	stub := &realmGrantStub{}
	server := New("unused")
	server.SetRealmGrantService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "caller", CanCreateRepositories: true})
	server.RegisterProjectedRepoPolicy("repo-1", "Shared", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)
	request := lifecycleRequest(contract.CmdRepoGrantAccess, contract.RepoGrantAccessPayload{ServerID: "office", RepoID: "repo-1", RecipientRealmID: "recipient", Access: "r"})
	if response := server.dispatch(request); response.Status == contract.StatusOK {
		t.Fatal("foreign repository grant accepted")
	}
	if stub.repoID != "" {
		t.Fatalf("forbidden grant reached service: %+v", stub)
	}
}
