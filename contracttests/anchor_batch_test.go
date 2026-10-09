package contracttests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filees/pkg/commit"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

type contractBatchProbe struct{ plans, writes int }

func TestAnchorBatchContractTreeFenceRequiresRecoveryWithoutRetry(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
	wc := t.TempDir()
	dir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, commit.RemoteTreePendingName), []byte("invalid evidence also fences"), 0600); err != nil {
		t.Fatal(err)
	}
	server.RegisterRepoAccess("repo", "file:///fixture/repo", wc, "server", contract.AccessReadWrite)
	probe := &contractBatchProbe{}
	server.SetAnchorBatch(probe)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "tree-fence-test")
	for _, action := range []string{"download", "release"} {
		p := contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: ".", Action: action}
		_, err := cli.AnchorPlan(ctx, p)
		var response *ipcclient.ResponseError
		if !errors.As(err, &response) || response.Body.Code != "HEAD-2019" || response.Body.MessageKey != "head.folder_recovery_required" || response.Body.Hint != "REQUIRE_ACTION" {
			t.Fatal(err, response)
		}
		_, err = cli.AnchorBatch(ctx, contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: strings.Repeat("ab", 32)})
		if !errors.As(err, &response) || response.Body.Code != "HEAD-2019" || response.Body.Hint != "REQUIRE_ACTION" {
			t.Fatal(err, response)
		}
	}
	if probe.plans != 0 || probe.writes != 0 {
		t.Fatal("pending tree reached provider", probe)
	}
}

func (b *contractBatchProbe) Plan(context.Context, contract.RepoAnchorPlanPayload) (contract.RepoAnchorPlan, error) {
	b.plans++
	return contract.RepoAnchorPlan{Directory: true, Files: 3, Bytes: 30, Fingerprint: strings.Repeat("ab", 32), SelectionFingerprint: strings.Repeat("cd", 32)}, nil
}
func (b *contractBatchProbe) Execute(context.Context, contract.RepoAnchorBatchPayload) (contract.RepoAnchorBatchResult, error) {
	b.writes++
	return contract.RepoAnchorBatchResult{State: "partial", Completed: 1, Total: 3, FailureCode: "HEAD-2018", FailureKey: "head.folder_busy"}, nil
}

func TestAnchorBatchContractRequiresActivationScopeAndConfirmation(t *testing.T) {
	socket := testSocketPath(t)
	server := ipcserver.New(socket)
	server.RegisterActivation(contract.ActivationStatus{ServerID: "server", ClientRole: contract.ClientRoleNormal})
	repo := server.RegisterRepoAccess("repo", "svn://example/repo", t.TempDir(), "server", "r")
	repo.SetDepthFunc(func(context.Context, string, string) error { t.Fatal("ordinary sparse fallback"); return nil })
	probe := &contractBatchProbe{}
	server.SetAnchorBatch(probe)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(socket, "batch-test")
	hello, err := cli.Hello(ctx)
	if err != nil || !slices.Contains(hello.Capabilities, contract.CapRepoAnchorBatch) {
		t.Fatal(hello, err)
	}
	p := contract.RepoAnchorPlanPayload{ServerID: "server", RepoID: "repo", Path: ".", Action: "download"}
	plan, err := cli.AnchorPlan(ctx, p)
	if err != nil || plan.Files != 3 || probe.writes != 0 || plan.SelectionFingerprint != strings.Repeat("cd", 32) {
		t.Fatal(plan, err)
	}
	if _, err := cli.AnchorBatch(ctx, contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p}); err == nil || probe.writes != 0 {
		t.Fatal("unconfirmed execution")
	}
	result, err := cli.AnchorBatch(ctx, contract.RepoAnchorBatchPayload{RepoAnchorPlanPayload: p, Fingerprint: plan.Fingerprint})
	if err != nil || result.State != "partial" || result.Completed != 1 || result.FailureCode != "HEAD-2018" || result.FailureKey != "head.folder_busy" {
		t.Fatal(result, err)
	}
	for _, path := range []string{"", "../a", ".svn/db", "dir/.FILEES/cache", "a\\b", "a\tb"} {
		p.Path = path
		if _, err := cli.AnchorPlan(ctx, p); err == nil {
			t.Fatal("invalid path accepted", path)
		}
	}
	p.Path = "."
	p.ServerID = "other"
	if _, err := cli.AnchorPlan(ctx, p); err == nil || probe.plans != 1 {
		t.Fatal("wrong activation admitted")
	}
	server.SetAnchorBatch(nil)
	hello, err = cli.Hello(ctx)
	if err != nil || slices.Contains(hello.Capabilities, contract.CapRepoAnchorBatch) {
		t.Fatal("absent service advertised", err)
	}
}
