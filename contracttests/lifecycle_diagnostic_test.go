package contracttests

import (
	"encoding/json"
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestLifecycleDiagnosticKeyRemainsSeparateFromLegacyText(t *testing.T) {
	for _, key := range []string{"", "repo.locate_other_repository"} {
		wanted := contract.RepoLifecycleResult{OperationID: "op", State: "attached", LastError: "original diagnostic", LastErrorKey: key}
		response := contract.OKResponse("request", wanted)
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var decoded contract.Response
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		var got contract.RepoLifecycleResult
		if err := contract.DecodePayload(decoded.Result, &got); err != nil {
			t.Fatal(err)
		}
		if got.LastErrorKey != key || got.LastError != wanted.LastError {
			t.Fatalf("diagnostic lost: %+v", got)
		}
	}
}
