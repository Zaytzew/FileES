package ipcserver

import (
	contract "filees/pkg/contract/v1"
	"os"
	"testing"
)

type shelfLifecycleStub struct {
	lifecycleStub
	selected contract.ShelfItem
	repoID   string
	parent   contract.RepoSummary
	err      error
}

func (s *shelfLifecycleStub) BeginShelfImport(serverID, repoID, url, path string, item contract.ShelfItem, parent contract.RepoSummary, destination string) (contract.RepoLifecycleResult, error) {
	s.parent = parent
	return s.BeginShelfFetch(serverID, repoID, url, path, item)
}

func TestShelfImportRequiresWritableAttachedParent(t *testing.T) {
	s := New("unused")
	local := &shelfLifecycleStub{}
	s.SetRepositoryLifecycleService(local)
	s.SetUploadChannelService(&uploadChannelStub{})
	s.RegisterActivation(contract.ActivationStatus{ServerID: "office", ClientRole: contract.ClientRoleNormal, RealmID: "owner", CanCreateRepositories: true})
	s.RegisterProjectedRepoPolicy("parent", "Parent", "svn://example/parent", "office", "rw", "active", "owner", "optional", false)
	shelf := s.RegisterProjectedRepoPolicy("upload-1", "Shelf", "svn://example/shelf", "office", "r", "active", "owner", "optional", false)
	shelf.SetPurpose(contract.RepoPurposeUploadShelf)
	p := contract.ShelfFetchPayload{ServerID: "office", RepoID: "parent", ChannelID: "channel-1", UploadID: "3f1d6a4e-0000-4000-8000-00000000beef", DestinationFolder: "destination"}
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Status == contract.StatusOK {
		t.Fatal("unattached parent accepted")
	}
	s.RegisterProjectedRepoPolicy("parent", "Parent", "svn://example/parent", "office", "r", "active", "owner", "optional", true)
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Status == contract.StatusOK {
		t.Fatal("read-only parent accepted")
	}
	s.RegisterProjectedRepoPolicy("parent", "Parent", "svn://example/parent", "office", "rw", "active", "owner", "optional", true)
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Status != contract.StatusOK {
		t.Fatalf("import: %+v", result)
	}
	if local.parent.ID != "parent" || local.parent.URL != "svn://example/parent" || local.repoID != "upload-1" {
		t.Fatal("source/destination authority mixed")
	}
}

func (s *shelfLifecycleStub) InspectShelf(serverID, repoID string) contract.RepoLifecycleResult {
	return contract.RepoLifecycleResult{ServerID: serverID, RepoID: repoID}
}
func (s *shelfLifecycleStub) BeginShelfFetch(serverID, repoID, url, path string, item contract.ShelfItem) (contract.RepoLifecycleResult, error) {
	if s.err != nil {
		return contract.RepoLifecycleResult{}, s.err
	}
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
	local.err = os.ErrExist
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Error == nil || result.Error.MessageKey != "repo.rename_target_exists" {
		t.Fatalf("folder collision lost its structured message: %+v", result)
	}
	local.err = nil
	p.UploadID = "forged"
	if result := s.dispatch(lifecycleRequest(contract.CmdRepoShelfFetch, p)); result.Status == contract.StatusOK {
		t.Fatal("unknown upload accepted")
	}
}
