package contracttests

import (
	"context"
	"encoding/json"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
	"testing"
)

func TestIntentResolutionContract(t *testing.T) {
	// Applying carries an opaque ID and a closed choice, never caller-edited
	// paths, hashes or a local filename to overwrite.
	b, err := json.Marshal(contract.IntentApplyPayload{PlanID: "opaque", Choice: contract.IntentDeleteAdd})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields["plan_id"] != "opaque" || fields["choice"] != "delete_add" {
		t.Fatal(fields)
	}
	if contract.CmdRepoIntentPlan == contract.CmdRepoIntentApply || contract.CapRepoIntentResolution == "" {
		t.Fatal("missing distinct plan/apply contract")
	}
}

func TestIntentResolutionIPCTransport(t *testing.T) {
	sock := testSocketPath(t)
	server := ipcserver.New(sock)
	repo := server.RegisterRepo("intent-docs", "svn://example/docs", t.TempDir())
	repo.SetIntentFuncs(func(context.Context) (*contract.IntentPlan, error) {
		return &contract.IntentPlan{PlanID: "opaque", RepoID: "intent-docs", Choice: contract.IntentDeleteAdd, Paths: []contract.IntentPath{{Path: "new V2.txt", Operation: "add", Size: 12, SHA256: "fixture"}}}, nil
	}, func(_ context.Context, id, choice string) (*contract.IntentApplyResult, error) {
		if id != "opaque" || choice != contract.IntentDeleteAdd {
			t.Errorf("changed decision: %q %q", id, choice)
		}
		return &contract.IntentApplyResult{PlanID: id, State: "queued"}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(sock, "intent-test")
	plan, err := cli.RepoIntentPlan(ctx, "intent-docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Paths) != 1 || plan.Paths[0].Path != "new V2.txt" {
		t.Fatalf("plan=%+v", plan)
	}
	result, err := cli.RepoIntentApply(ctx, plan.RepoID, plan.PlanID, plan.Choice)
	if err != nil || result.State != "queued" || result.PlanID != plan.PlanID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	repo.SetProjection("svn://example/docs", "ro")
	if _, err := cli.RepoIntentApply(ctx, plan.RepoID, plan.PlanID, plan.Choice); err == nil {
		t.Fatal("transport bypassed read-only gate")
	}
}
