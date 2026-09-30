package ipcserver

import (
	"context"
	"fmt"
	"testing"

	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
)

type oversizedShareService struct{ publicShareStub }

func (*oversizedShareService) CreatePublicShare(context.Context, string, contract.PublicShareDeclaration) (contract.PublicShareResult, error) {
	return contract.PublicShareResult{}, fmt.Errorf("transport: %w", control.ErrTicketTooLarge)
}
func (*oversizedShareService) UpdatePublicShare(context.Context, string, string, contract.PublicShareDeclaration, bool) (contract.PublicShareResult, error) {
	return contract.PublicShareResult{}, control.ErrTicketTooLarge
}
func (*oversizedShareService) ListPublicShares(context.Context, string, string) ([]contract.PublicShareSummary, error) {
	return nil, fmt.Errorf("transport: %w", control.ErrResultTooLarge)
}

func TestPublicShareSizeErrorsKeepSpecificMessage(t *testing.T) {
	server := New("unused")
	server.SetPublicShareService(&oversizedShareService{})
	server.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo-1", "office", "rw", "active", "owner", "optional", true)
	declaration := contract.PublicShareDeclaration{RepoID: "repo-1"}
	for _, test := range []struct {
		command   string
		payload   any
		code, key string
	}{
		{contract.CmdRepoPublicShareCreate, contract.PublicShareCreatePayload{ServerID: "office", PublicShareDeclaration: declaration}, "SHARE-1003", "public_share.request_too_large"},
		{contract.CmdRepoPublicShareUpdate, contract.PublicShareUpdatePayload{ServerID: "office", ChannelID: "channel-1", PublicShareDeclaration: declaration}, "SHARE-1003", "public_share.request_too_large"},
		{contract.CmdRepoPublicShareList, contract.PublicShareListPayload{ServerID: "office", RepoID: "repo-1"}, "SHARE-1004", "public_share.result_too_large"},
	} {
		t.Run(test.command, func(t *testing.T) {
			response := server.dispatch(lifecycleRequest(test.command, test.payload))
			if response.Error == nil || response.Error.Code != test.code || response.Error.MessageKey != test.key {
				t.Fatalf("lost cause: %+v", response)
			}
		})
	}
}
