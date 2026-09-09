package contracttests

import (
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"testing"
)

func TestLastCommitDateCrossesIPCIndependentOfReceipt(t *testing.T) {
	want := contract.RepoStatus{RepoID: "r", LastCommitAt: "2026-08-01T10:00:00Z", LastSyncAt: "2026-09-09T12:00:00Z"}
	sock := startFakeDaemon(t, func(req contract.Request) contract.Response { return contract.OKResponse(req.RequestID, want) })
	got, err := ipcclient.New(sock, "test").RepoStatus(t.Context(), "r")
	if err != nil || got.LastCommitAt != want.LastCommitAt || got.LastSyncAt != want.LastSyncAt {
		t.Fatalf("date projection: %+v %v", got, err)
	}
}
