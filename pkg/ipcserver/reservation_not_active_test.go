package ipcserver

import (
	"context"
	"testing"

	contract "filees/pkg/contract/v1"
)

// A repository whose first publication still runs is no reservation source:
// it used to be an unknown one, and as the server's only repository it turned
// the lock tile into "0+?" for the whole import (2026-09-24).
func TestReservationListLeavesOutRepositoriesNotYetActive(t *testing.T) {
	server := New("unused")
	creating := server.RegisterRepoAccess("new", "svn+ssh://host/new", "/work/new", "demo", contract.AccessReadWrite)
	creating.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{NotActive: true}, nil
	}, nil)

	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "demo"}))
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	if len(result.Sources) != 0 || len(result.Reservations) != 0 {
		t.Fatalf("a repository not yet active became a source: %+v", result)
	}

	unknown := server.RegisterRepoAccess("old", "svn+ssh://host/old", "/work/old", "demo", contract.AccessReadWrite)
	unknown.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{Unknown: true}, nil
	}, nil)
	response = server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "demo"}))
	result = contract.RepoReservationListResult{}
	decodeIPCResult(t, response, &result)
	if len(result.Sources) != 1 || sourceFor(t, result, "old").State != contract.ReservationSourceUnknown {
		t.Fatalf("an unanswered active repository must stay unknown: %+v", result.Sources)
	}
}
