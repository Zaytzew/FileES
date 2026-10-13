package contracttests

import (
	"context"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"testing"
)

func TestIPCLocalPresenceDoesNotMeanAttachedOrWritable(t *testing.T) {
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response {
		return contract.OKResponse(req.RequestID, contract.RepoListResult{Repos: []contract.RepoSummary{{
			ID: "local", ServerID: "offline-server", LocalPath: "/local/project", State: contract.StateInitializing,
			LocalCopy: &contract.LocalCopySummary{Path: "/local/project", Kind: "virtual", Presence: "present", Lifecycle: "attached"},
		}}})
	})
	result, err := ipcclient.New(sock, "gui").RepoList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := result.Repos[0]
	if row.Attached || row.Access != "" || row.LocalCopy == nil || row.LocalCopy.Presence != "present" || row.LocalCopy.Kind != "virtual" {
		t.Fatalf("local observation changed semantics: %+v", row)
	}
}
