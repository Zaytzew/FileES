package contracttests

import (
	"encoding/json"
	contract "filees/pkg/contract/v1"
	"strings"
	"testing"
)

func TestShelfDownloadContractCarriesSelectionAndDurableReceiptNotBytes(t *testing.T) {
	p := contract.ShelfFetchPayload{ServerID: "server", RepoID: "parent", ChannelID: "channel", UploadID: "upload"}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"repo_path", "sha256", "payload", "repo_url"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("client supplies authority or content")
		}
	}
	result := contract.RepoLifecycleResult{OperationID: "op", FetchID: "fetch", FetchUploadID: "upload", FetchState: "complete"}
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got contract.RepoLifecycleResult
	if err := json.Unmarshal(raw, &got); err != nil || got.FetchState != "complete" || got.FetchID != "fetch" {
		t.Fatal("lost durable receipt")
	}
	if contract.CmdRepoShelfFetch != contract.CapRepoShelfFetch {
		t.Fatal("command/capability diverged")
	}
}
