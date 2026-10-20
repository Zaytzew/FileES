package contracttests

import (
	"encoding/json"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func TestAnchorRuntimeStatusSurvivesIPCAndClearsOnRecovery(t *testing.T) {
	sock := testSocketPath(t)
	s := ipcserver.New(sock)
	r := s.RegisterRepoAccess("repo", "svn://fixture/repo", t.TempDir(), "server", "rw")
	value := &contract.AnchorRuntimeStatus{Phase: "failed", Since: "2026-10-20T11:00:00Z", Detail: "register_sync_root 0x80070178"}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := ipcclient.New(sock, "test")
	for _, phase := range []string{"failed", "preparing", "connecting", "ready"} {
		want := *value
		want.Phase = phase
		r.SetAnchorRuntimeSource(func() *contract.AnchorRuntimeStatus { copy := want; return &copy })
		got, err := c.RepoStatus(t.Context(), "repo")
		if err != nil || got.AnchorRuntime == nil || *got.AnchorRuntime != want {
			t.Fatal(got, err)
		}
	}
	r.SetAnchorRuntimeSource(nil)
	got, err := c.RepoStatus(t.Context(), "repo")
	if err != nil || got.AnchorRuntime != nil {
		t.Fatal(got, err)
	}
}

func TestAnchorIntegrityPartialResultCarriesTheAffectedPath(t *testing.T) {
	want := contract.RepoAnchorBatchResult{State: "partial", FailureCode: "SYNC-2025", FailureKey: "sync.download_integrity_held", FailurePath: "folder/OPZ.pdf"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got contract.RepoAnchorBatchResult
	if err := json.Unmarshal(raw, &got); err != nil || got != want {
		t.Fatal(got, err)
	}
}
