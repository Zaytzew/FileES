package contracttests

import (
	"encoding/json"
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestConflictRecoveryWireCarriesClosedChoiceAndPreservedCopy(t *testing.T) {
	plan := contract.CommitRecoveryPlan{PlanID: "opaque", RepoID: "repo", Choice: contract.CommitRecoveryServerCopy, Conflicts: []string{"docs/plan.dwg"}, ConflictCopy: "!kolizje/conflicted-copy-opaque"}
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var got contract.CommitRecoveryPlan
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Choice == contract.CommitRecoveryRetryQueue || got.Choice != plan.Choice || len(got.Conflicts) != 1 || got.Conflicts[0] != plan.Conflicts[0] || got.ConflictCopy != plan.ConflictCopy {
		t.Fatalf("decision lost %s", b)
	}
}
