package contracttests

import (
	"encoding/json"
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestRelocationQueueKeepsOldAndPendingPathsDistinct(t *testing.T) {
	wanted := contract.RepoLifecycleResult{OperationID: "same-operation", State: contract.LifecycleRelocationQueued, LocalPath: "old", PendingLocalPath: "new"}
	raw, err := json.Marshal(wanted)
	if err != nil {
		t.Fatal(err)
	}
	var got contract.RepoLifecycleResult
	if err := contract.DecodePayload(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.State == "attached" || got.State != contract.LifecycleRelocationQueued || got.LocalPath != wanted.LocalPath || got.PendingLocalPath != wanted.PendingLocalPath || got.LastError != "" {
		t.Fatal(got)
	}
}
