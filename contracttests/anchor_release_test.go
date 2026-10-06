package contracttests

import (
	"context"
	"errors"
	"slices"
	"testing"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func TestAnchorReleaseContractHasNoSparseFallback(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
	repo := server.RegisterRepoAccess("repo", "svn://example/repo", t.TempDir(), "server", "r")
	repo.SetDepthFunc(func(context.Context, string, string) error { t.Error("ordinary sparse mutation invoked"); return nil })
	calls := 0
	server.SetAnchorRelease(func(_ context.Context, sid, rid, rel string) error {
		calls++
		if sid != "server" || rid != "repo" {
			t.Errorf("wrong activated identity: %s/%s", sid, rid)
		}
		if rel == "pending" {
			return commit.ErrFileReleasePending
		}
		if rel == "modified" {
			return errors.New("changed local data")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "release-probe")
	hello, err := cli.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapRepoAnchorRelease) {
		t.Fatal("connected provider capability missing", hello, err)
	}
	result, err := cli.AnchorRelease(ctx, contract.RepoAnchorReleasePayload{ServerID: "server", RepoID: "repo", Path: "file.txt"})
	if err != nil || result.State != "attached" || result.Path != "file.txt" {
		t.Fatal(result, err)
	}
	for _, rel := range []string{"../file", ".svn/wc.db", "dir/.FILEES/state", "file\tother", "file\nother", "dir\\file", ".", ""} {
		if _, err := cli.AnchorRelease(ctx, contract.RepoAnchorReleasePayload{ServerID: "server", RepoID: "repo", Path: rel}); err == nil {
			t.Errorf("invalid path accepted: %q", rel)
		}
	}
	if calls != 1 {
		t.Fatal("invalid selections reached provider", calls)
	}
	for rel, key := range map[string]string{"pending": "head.release_pending", "modified": "head.release_refused"} {
		_, err := cli.AnchorRelease(ctx, contract.RepoAnchorReleasePayload{ServerID: "server", RepoID: "repo", Path: rel})
		var refusal *ipcclient.ResponseError
		if !errors.As(err, &refusal) {
			t.Fatal(err)
		}
		_, _, _, got := refusal.PresentationError()
		if got != key {
			t.Fatal(got, key)
		}
	}
	before := calls
	if _, err := cli.AnchorRelease(ctx, contract.RepoAnchorReleasePayload{ServerID: "other", RepoID: "repo", Path: "file.txt"}); err == nil || calls != before {
		t.Fatal("unactivated identity reached provider")
	}
	server.SetAnchorRelease(nil)
	hello, err = cli.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapRepoAnchorRelease) {
		t.Fatal("missing provider still advertised", hello, err)
	}
	if _, err := cli.AnchorRelease(ctx, contract.RepoAnchorReleasePayload{ServerID: "server", RepoID: "repo", Path: "file.txt"}); err == nil || calls != before {
		t.Fatal("missing provider accepted release")
	}
}
