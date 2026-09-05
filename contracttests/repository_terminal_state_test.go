package contracttests

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

func TestIPCRemoteDeletionPreservesLocalCopyEvidence(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		return contract.OKResponse(req.RequestID, contract.RepoListResult{Repos: []contract.RepoSummary{{
			ID: "deleted-repo", ServerDeleted: true, LocalPath: "/preserved/docs",
			LocalCopyPreserved: true, LocalCopyStatus: "changed",
		}}})
	})
	result, err := ipcclient.New(sock, "clone").RepoList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repos) != 1 {
		t.Fatalf("repos = %+v", result)
	}
	r := result.Repos[0]
	if !r.ServerDeleted || !r.LocalCopyPreserved || r.LocalCopyStatus != "changed" || r.Attached || r.LocalPath != "/preserved/docs" {
		t.Fatalf("terminal evidence lost over IPC: %+v", r)
	}
}
