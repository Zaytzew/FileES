package ipcserver

import (
	"context"
	contract "filees/pkg/contract/v1"
	"testing"
)

type invitationShareStub struct{ publicShareStub }

func (s *invitationShareStub) ListPublicSharesWithInvitations(ctx context.Context, server, repo string) (contract.PublicShareListResult, error) {
	shares, err := s.ListPublicShares(ctx, server, repo)
	shares[0].InvitationMessage = "Saved"
	return contract.PublicShareListResult{Shares: shares, CanManageInvitations: true}, err
}
func TestPublicShareInvitationIPC(t *testing.T) {
	stub := &invitationShareStub{}
	s := New("unused")
	s.SetPublicShareService(stub)
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
	s.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)
	r := s.dispatch(lifecycleRequest(contract.CmdRepoPublicShareList, contract.PublicShareListPayload{ServerID: "office", RepoID: "repo-1"}))
	var list contract.PublicShareListResult
	if r.Status != contract.StatusOK || contract.DecodeResult(r.Result, &list) != nil || !list.CanManageInvitations || list.Shares[0].InvitationMessage != "Saved" {
		t.Fatal(r, list)
	}
	d := contract.PublicShareDeclaration{RepoID: "repo-1", SourceRoot: ".", Slug: "share", Recipients: []string{"a@example.test"}, ManageInvitations: true, ResendInvitations: true, InvitationMessage: "Saved"}
	r = s.dispatch(lifecycleRequest(contract.CmdRepoPublicShareUpdate, contract.PublicShareUpdatePayload{ServerID: "office", ChannelID: "channel-1", PublicShareDeclaration: d}))
	if r.Status != contract.StatusOK || !stub.declaration.ResendInvitations || !stub.declaration.ManageInvitations || stub.declaration.InvitationMessage != "Saved" {
		t.Fatal(r, stub)
	}
}
