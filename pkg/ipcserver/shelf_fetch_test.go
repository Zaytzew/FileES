package ipcserver

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

type shelfLifecycleStub struct {
	lifecycleStub
	selected contract.ShelfItem
	repoID   string
}

func (s *shelfLifecycleStub) InspectShelf(serverID, repoID string) contract.RepoLifecycleResult {
	return contract.RepoLifecycleResult{ServerID: serverID, RepoID: repoID}
}
func (s *shelfLifecycleStub) BeginShelfFetch(serverID, repoID, url, path string, item contract.ShelfItem) (contract.RepoLifecycleResult, error) {
	s.repoID, s.selected = repoID, item
	return contract.RepoLifecycleResult{OperationID: "op", FetchID: "fetch", FetchState: "queued"}, nil
}
func TestShelfFetchResolvesReceiptAndRequiresReadOnlyPurpose(t *testing.T) {
	s := New("unused")
	local := &shelfLifecycleStub{}
	s.SetRepositoryLifecycleService(local)
	s.SetUploadChannelService(&uploadChannelStub{})
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
	s.RegisterProjectedRepoPolicy("parent", "Parent", "svn://example/parent", "office", "rw", "active", "owner", "optional", true)
	repo := s.RegisterProjectedRepoPolicy("upload-1", "Shelf", "svn://example/shelf", "office", "r", "active", "owner", "optional", false)
	p := contract.ShelfFetchPayload{ServerID: "office", RepoID: "parent", ChannelID: "channel-1", UploadID: "3f1d6a4e-0000-4000-8000-00000000beef", LocalPath: "unused"}
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Status == contract.StatusOK {
		t.Fatal("ordinary repo accepted")
	}
	repo.SetPurpose(contract.RepoPurposeUploadShelf)
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Status != contract.StatusOK {
		t.Fatalf("fetch: %+v", result)
	}
	if local.repoID != "upload-1" || local.selected.RepoPath != "rzut.dwg" {
		t.Fatal("selection did not come from authority")
	}
	p.UploadID = "forged"
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Status == contract.StatusOK {
		t.Fatal("unknown upload accepted")
	}
}
