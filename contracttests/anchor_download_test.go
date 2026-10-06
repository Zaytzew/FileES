package contracttests

import (
	"context"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

// Existing request and result envelopes remain valid for explicit CFAPI
// downloads. A provider error must not be hidden by ordinary SVN depth.
func TestAnchorDownloadUsesExistingHeadContract(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
	repo := server.RegisterRepoAccess("repo", "svn://example/repo", t.TempDir(), "server", "r")
	depthCalls := 0
	repo.SetDepthFunc(func(context.Context, string, string) error { depthCalls++; return nil })
	server.SetAnchorDownload(func(_ context.Context, sid, rid, rel string) (bool, error) {
		if sid != "server" || rid != "repo" {
			t.Fatalf("identity=%s/%s", sid, rid)
		}
		if rel == "refused.txt" {
			return true, errors.New("provider refused")
		}
		return true, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "anchor-probe")
	result, err := cli.HeadMaterialize(ctx, contract.RepoHeadMaterializePayload{ServerID: "server", RepoID: "repo", Path: "file.txt"})
	if err != nil || result.State != "attached" || result.Path != "file.txt" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := cli.HeadMaterialize(ctx, contract.RepoHeadMaterializePayload{ServerID: "server", RepoID: "repo", Path: "refused.txt"}); err == nil {
		t.Fatal("provider failure became success")
	}
	if depthCalls != 0 {
		t.Fatalf("SVN fallback calls=%d", depthCalls)
	}
}
