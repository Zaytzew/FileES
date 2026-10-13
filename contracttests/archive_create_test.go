package contracttests

import (
	"encoding/json"
	contract "filees/pkg/contract/v1"
	"strings"
	"testing"
)

func TestCreateArchiveIsAnOptionalLocalIntentNotAServerPath(t *testing.T) {
	legacy := contract.RepoCreateRequestPayload{ServerID: "office", DisplayName: "Project", LocalPath: "/wc"}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "archive_path") {
		t.Fatal("legacy intent changed wire format")
	}
	legacy.ArchivePath = "/backup/project.dmp"
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded contract.RepoCreateRequestPayload
	if err := contract.DecodePayload(raw, &decoded); err != nil || decoded != legacy {
		t.Fatalf("round trip=%+v err=%v", decoded, err)
	}
	if contract.CapRepoCreateArchive == contract.CapRepoCreateRequest {
		t.Fatal("old daemon cannot distinguish import support")
	}
}
