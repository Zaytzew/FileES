package ipcserver

import (
	"errors"
	"path/filepath"
	"testing"

	contract "filees/pkg/contract/v1"
)

type anchorLifecycleStub struct {
	lifecycleStub
	anchorLocal string
	anchorCalls int
}

func (s *anchorLifecycleStub) BeginAnchorAttach(_, _, localPath string, _ bool) (contract.RepoLifecycleResult, error) {
	s.anchorCalls++
	s.anchorLocal = localPath
	return contract.RepoLifecycleResult{OperationID: "anchor-op", LocalPath: localPath, State: "unattached"}, nil
}

func (s *anchorLifecycleStub) ApproveAttach(operationID, _, _, _, _ string) (contract.RepoLifecycleResult, error) {
	s.approveCalls++
	return contract.RepoLifecycleResult{OperationID: operationID, LocalPath: s.anchorLocal, State: "attaching"}, nil
}

func anchorServer(t *testing.T, check AnchorPrecheck) (*Server, *anchorLifecycleStub) {
	t.Helper()
	server := New("unused")
	stub := &anchorLifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	if check != nil {
		server.SetAnchorPrecheck(check)
	}
	server.RegisterActivation(contract.ActivationStatus{ServerID: "primary", ClientRole: contract.ClientRoleNormal})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo", "primary", "rw", "active", "owner", "optional", false)
	return server, stub
}

func TestAnAnchorIsAnAttachmentWithNothingChosen(t *testing.T) {
	var checked string
	server, stub := anchorServer(t, func(local string) error { checked = local; return nil })
	folder := filepath.Join(t.TempDir(), "Atlas")
	result := headDecode[contract.RepoHeadWriteResult](t, server.dispatch(lifecycleRequest(contract.CmdRepoAnchorCreate,
		contract.RepoAnchorCreatePayload{ServerID: "primary", RepoID: "repo-1", LocalPath: folder})))
	if checked != folder {
		t.Fatalf("the folder was not checked before anything was recorded: %q", checked)
	}
	if stub.anchorCalls != 1 || stub.approveCalls != 1 || stub.anchorLocal != folder {
		t.Fatalf("anchor attach calls=%d approve=%d local=%q", stub.anchorCalls, stub.approveCalls, stub.anchorLocal)
	}
	if result.State != "attaching" || result.OperationID != "anchor-op" {
		t.Fatalf("result = %+v", result)
	}
	if !hasCapability(server.capabilities(), contract.CapRepoExplorerAnchor) {
		t.Fatal("a daemon that can make anchors must say so")
	}
}

func TestAFolderInsideAnotherProviderIsRefusedBeforeAnythingIsRecorded(t *testing.T) {
	server, stub := anchorServer(t, func(string) error { return errors.New("inside a folder synchronised by Nextcloud") })
	response := server.dispatch(lifecycleRequest(contract.CmdRepoAnchorCreate,
		contract.RepoAnchorCreatePayload{ServerID: "primary", RepoID: "repo-1", LocalPath: `D:\ATMPROJEKT\Atlas`}))
	if response.Status == contract.StatusOK || response.Error.MessageKey != "head.anchor_refused" {
		t.Fatalf("response = %+v", response)
	}
	if stub.anchorCalls != 0 {
		t.Fatal("a refused folder must not start an attachment")
	}
}

func TestNoAnchorsWithoutTheHelper(t *testing.T) {
	server, stub := anchorServer(t, nil)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoAnchorCreate,
		contract.RepoAnchorCreatePayload{ServerID: "primary", RepoID: "repo-1", LocalPath: filepath.Join(t.TempDir(), "Atlas")}))
	if response.Status == contract.StatusOK || response.Error.MessageKey != "head.anchor_unavailable" || stub.anchorCalls != 0 {
		t.Fatalf("response = %+v calls=%d", response, stub.anchorCalls)
	}
	if hasCapability(server.capabilities(), contract.CapRepoExplorerAnchor) {
		t.Fatal("capability advertised without the helper")
	}
}

func TestAnAnchorNeedsTheSameTrustAsBrowsing(t *testing.T) {
	server := New("unused")
	server.SetAnchorPrecheck(func(string) error { return nil })
	server.SetRepositoryLifecycleService(&anchorLifecycleStub{})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo", "primary", "rw", "active", "owner", "optional", false)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoAnchorCreate,
		contract.RepoAnchorCreatePayload{ServerID: "primary", RepoID: "repo-1", LocalPath: filepath.Join(t.TempDir(), "Atlas")}))
	if response.Status == contract.StatusOK {
		t.Fatal("an anchor was created without an activation")
	}
}
