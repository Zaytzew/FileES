package ipcserver

import (
	"encoding/json"
	"testing"
	"time"

	"filees/pkg/activity"
	contract "filees/pkg/contract/v1"
)

type activitySourceStub struct{ entries []activity.Entry }

func (s activitySourceStub) List() []activity.Entry {
	return append([]activity.Entry(nil), s.entries...)
}

func TestRepoActivityIsCapabilityGatedGlobalSnapshot(t *testing.T) {
	server := New(t.TempDir() + "/daemon.sock")
	for _, capability := range server.capabilities() {
		if capability == contract.CapRepoActivity {
			t.Fatal("activity advertised without a source")
		}
	}
	now := time.Date(2026, 7, 22, 14, 37, 0, 0, time.UTC)
	server.SetActivitySource(activitySourceStub{entries: []activity.Entry{
		{RepoID: "docs", Path: "report.pdf", Kind: activity.Added, Stage: activity.Published, DetectedAt: now, UpdatedAt: now, Revision: 18},
		{RepoID: "photos", Path: "summer.jpg", Kind: activity.Modified, Stage: activity.Pending, DetectedAt: now, UpdatedAt: now},
	}})
	payload, _ := json.Marshal(contract.RepoActivityPayload{Limit: 1})
	response := server.dispatch(contract.Request{RequestID: "activity", Command: contract.CmdRepoActivity, Payload: payload})
	if response.Status != contract.StatusOK {
		t.Fatalf("response=%+v", response)
	}
	var result contract.RepoActivityResult
	if err := contract.DecodeResult(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].RepoID != "docs" || result.Entries[0].Revision != 18 {
		t.Fatalf("activity=%+v", result.Entries)
	}
	found := false
	for _, capability := range server.capabilities() {
		found = found || capability == contract.CapRepoActivity
	}
	if !found {
		t.Fatal("activity capability missing with configured source")
	}
}
