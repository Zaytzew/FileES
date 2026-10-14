package contracttests

import (
	"encoding/json"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
)

func TestReconcileProgressRoundTrip(t *testing.T) {
	original := contract.RepoStatus{RepoID: "one", ReconcileProgress: &contract.ReconcileProgress{Phase: "adopting", Done: 16, Total: 40}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var got contract.RepoStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ReconcileProgress == nil || *got.ReconcileProgress != *original.ReconcileProgress {
		t.Fatal(string(raw))
	}
	var old contract.RepoStatus
	if err := json.Unmarshal([]byte(`{"repo_id":"old"}`), &old); err != nil || old.ReconcileProgress != nil {
		t.Fatal(err)
	}
}

func TestReconcileProgressCrossesIPCAndClears(t *testing.T) {
	server, _, sock := startEventServer(t)
	rs := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", t.TempDir(), "office", contract.AccessReadWrite)
	c := ipcclient.New(sock, "gui")
	rs.SetReconcileProgress(&contract.ReconcileProgress{Phase: "adopting", Done: 16, Total: 40})
	got, err := c.RepoStatus(t.Context(), "docs")
	if err != nil || got.ReconcileProgress == nil || got.ReconcileProgress.Done != 16 {
		t.Fatal(got, err)
	}
	rs.SetReconcileProgress(nil)
	got, err = c.RepoStatus(t.Context(), "docs")
	if err != nil || got.ReconcileProgress != nil {
		t.Fatal(got, err)
	}
}
