package contracttests

import (
	"encoding/json"
	"filees/internal/domaincatalog"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
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

func TestCommitRecoveryRefusalsHaveDomainMessages(t *testing.T) {
	registry, err := domaincatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"commit.recovery_refused", "commit.recovery_unavailable"} {
		spec, ok := errcat.ByKey(errcat.Key(key))
		if !ok || spec.Code != errcat.CodeCommitFail {
			t.Fatalf("wire key absent: %s", key)
		}
		response := contract.ErrResponse("test", "COMMIT-3100", "ERROR", "REQUIRE_ACTION", key, nil)
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) == 0 {
			t.Fatal("empty response")
		}
		for _, lang := range []string{"pl", "en", "de", "es", "fr"} {
			message, ok := registry.Message(lang, key)
			if !ok || len(message.Templates()) == 0 {
				t.Fatalf("missing %s/%s", lang, key)
			}
		}
	}
}
