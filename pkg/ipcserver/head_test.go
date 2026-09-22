package ipcserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contract "filees/pkg/contract/v1"
)

type headStub struct {
	listed, catted []string
	entries        []contract.RepoHeadEntry
}

func (h *headStub) HeadList(_ context.Context, _, _, path string) ([]contract.RepoHeadEntry, error) {
	h.listed = append(h.listed, path)
	return append([]contract.RepoHeadEntry(nil), h.entries...), nil
}

func (h *headStub) HeadCat(_ context.Context, _, _, path string) (string, error) {
	h.catted = append(h.catted, path)
	return "/tmp/filees-head/" + path, nil
}

type sparseLifecycleStub struct {
	lifecycleStub
	sparseLocal, sparsePath string
	sparseCalls, fullDepth  int
}

func (s *sparseLifecycleStub) BeginSparseAttach(_, _, localPath, sparsePath string, _ bool) (contract.RepoLifecycleResult, error) {
	s.sparseCalls++
	s.sparseLocal, s.sparsePath = localPath, sparsePath
	return contract.RepoLifecycleResult{OperationID: "sparse-op", LocalPath: localPath, State: "unattached"}, nil
}

func (s *sparseLifecycleStub) ApproveAttach(operationID, serverID, repoID, repoURL, access string) (contract.RepoLifecycleResult, error) {
	s.approveCalls++
	return contract.RepoLifecycleResult{OperationID: operationID, LocalPath: s.sparseLocal, State: "attaching"}, nil
}

func (s *sparseLifecycleStub) MarkFullDepth(_, _ string) error {
	s.fullDepth++
	return nil
}

func headDecode[T any](t *testing.T, response contract.Response) T {
	t.Helper()
	if response.Status != contract.StatusOK {
		t.Fatalf("refused: %+v", response.Error)
	}
	raw, _ := json.Marshal(response.Result)
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Browsing needs a live activation and read access from the projection; a
// desktop awaiting approval sees the repository list but not its tree
// (implementation notes (not distributed), trust boundary).
func TestHeadBrowsingRequiresAnActivationAndReadAccess(t *testing.T) {
	server := New("unused")
	server.SetHeadService(&headStub{})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo", "primary", "rw", "active", "owner", "optional", false)
	list := lifecycleRequest(contract.CmdRepoHeadList, contract.RepoHeadListPayload{ServerID: "primary", RepoID: "repo-1"})
	if response := server.dispatch(list); response.Status == contract.StatusOK {
		t.Fatal("tree listed without an activation")
	}
	server.RegisterActivation(contract.ActivationStatus{ServerID: "primary", ClientRole: contract.ClientRoleNormal})
	if response := server.dispatch(list); response.Status != contract.StatusOK {
		t.Fatalf("activated member refused: %+v", response.Error)
	}
	server.RegisterProjectedRepoPolicy("repo-2", "Hidden", "svn://example/hidden", "primary", "", "active", "owner", "optional", false)
	hidden := lifecycleRequest(contract.CmdRepoHeadList, contract.RepoHeadListPayload{ServerID: "primary", RepoID: "repo-2"})
	if response := server.dispatch(hidden); response.Status == contract.StatusOK {
		t.Fatal("tree listed without read access")
	}
	escape := lifecycleRequest(contract.CmdRepoHeadList, contract.RepoHeadListPayload{ServerID: "primary", RepoID: "repo-1", Path: "a/../../etc"})
	if response := server.dispatch(escape); response.Status == contract.StatusOK {
		t.Fatal("path with .. accepted")
	}
}

func TestHeadListMarksWhatIsAlreadyOnThisComputer(t *testing.T) {
	wc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wc, "art", "chosen"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := New("unused")
	server.SetHeadService(&headStub{entries: []contract.RepoHeadEntry{{Name: "chosen", Kind: "dir"}, {Name: "remote.psd", Kind: "file", Size: 9}}})
	server.RegisterActivation(contract.ActivationStatus{ServerID: "primary", ClientRole: contract.ClientRoleNormal})
	server.RegisterRepoAccess("repo-1", "svn://example/repo", wc, "primary", "rw")
	result := headDecode[contract.RepoHeadListResult](t, server.dispatch(lifecycleRequest(contract.CmdRepoHeadList, contract.RepoHeadListPayload{ServerID: "primary", RepoID: "repo-1", Path: "art"})))
	if len(result.Entries) != 2 || !result.Entries[0].Local || result.Entries[1].Local {
		t.Fatalf("local marks = %+v", result.Entries)
	}
}

// The first chosen path starts a sparse attachment through the lifecycle, so
// the copy is supervised from its first moment - never a bare checkout.
func TestFirstChosenPathStartsASparseAttachment(t *testing.T) {
	server := New("unused")
	stub := &sparseLifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "primary", ClientRole: contract.ClientRoleNormal})
	server.RegisterProjectedRepoPolicy("repo-1", "Docs", "svn://example/repo", "primary", "rw", "active", "owner", "optional", false)

	noAnchor := lifecycleRequest(contract.CmdRepoHeadMaterialize, contract.RepoHeadMaterializePayload{ServerID: "primary", RepoID: "repo-1", Path: "art/chosen"})
	if response := server.dispatch(noAnchor); response.Status == contract.StatusOK || response.Error == nil || response.Error.MessageKey != "head.anchor_required" {
		t.Fatalf("first path without an anchor: %+v", response)
	}
	anchor := filepath.Join(t.TempDir(), "Docs")
	result := headDecode[contract.RepoHeadWriteResult](t, server.dispatch(lifecycleRequest(contract.CmdRepoHeadMaterialize, contract.RepoHeadMaterializePayload{ServerID: "primary", RepoID: "repo-1", Path: "/art/chosen/", LocalPath: anchor})))
	if stub.sparseCalls != 1 || stub.approveCalls != 1 || stub.sparsePath != "art/chosen" || stub.sparseLocal != anchor {
		t.Fatalf("sparse attach calls=%d approve=%d path=%q local=%q", stub.sparseCalls, stub.approveCalls, stub.sparsePath, stub.sparseLocal)
	}
	if result.State != "attaching" || result.OperationID != "sparse-op" {
		t.Fatalf("result = %+v", result)
	}
}

// Later paths and "whole folder" deepen the running copy in place, through the
// commit service's lock, and never ask for another folder.
func TestLaterPathsAndFillDeepenTheSameCopy(t *testing.T) {
	wc := t.TempDir()
	server := New("unused")
	stub := &sparseLifecycleStub{}
	server.SetRepositoryLifecycleService(stub)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "primary", ClientRole: contract.ClientRoleNormal})
	repo := server.RegisterRepoAccess("repo-1", "svn://example/repo", wc, "primary", "rw")
	repo.SetSparse(true)

	materialize := lifecycleRequest(contract.CmdRepoHeadMaterialize, contract.RepoHeadMaterializePayload{ServerID: "primary", RepoID: "repo-1", Path: "docs"})
	if response := server.dispatch(materialize); response.Status == contract.StatusOK || response.Error.MessageKey != "head.copy_not_running" {
		t.Fatalf("deepen without a running copy: %+v", response)
	}
	var calls [][2]string
	repo.SetDepthFunc(func(_ context.Context, rel, depth string) error {
		calls = append(calls, [2]string{rel, depth})
		return nil
	})
	result := headDecode[contract.RepoHeadWriteResult](t, server.dispatch(materialize))
	if result.State != "attached" || result.LocalPath != wc || stub.sparseCalls != 0 {
		t.Fatalf("later path result=%+v sparse attaches=%d", result, stub.sparseCalls)
	}
	headDecode[contract.RepoHeadWriteResult](t, server.dispatch(lifecycleRequest(contract.CmdRepoHeadFill, contract.RepoHeadFillPayload{ServerID: "primary", RepoID: "repo-1"})))
	if len(calls) != 2 || calls[0] != [2]string{"docs", "infinity"} || calls[1] != [2]string{".", "infinity"} {
		t.Fatalf("depth calls = %v", calls)
	}
	if stub.fullDepth != 1 || repo.Snapshot().Sparse {
		t.Fatalf("fill did not record the whole tree: fullDepth=%d sparse=%v", stub.fullDepth, repo.Snapshot().Sparse)
	}
}
