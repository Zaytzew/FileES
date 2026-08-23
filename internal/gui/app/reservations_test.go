package app

import (
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestProjectedReservationIDSelectsOneLockGeneration(t *testing.T) {
	first := projectReservation("server-1", contract.Reservation{
		RepoID: "repo-1", WorkingCopy: `/wc/repo-1`, Path: "plan.dwg",
		Token: "opaque-generation-a", CanRelease: true,
	})
	same := projectReservation("server-1", contract.Reservation{
		RepoID: "repo-1", WorkingCopy: `/wc/repo-1`, Path: "plan.dwg",
		Token: "opaque-generation-a", CanRelease: true,
	})
	newer := projectReservation("server-1", contract.Reservation{
		RepoID: "repo-1", WorkingCopy: `/wc/repo-1`, Path: "plan.dwg",
		Token: "opaque-generation-b", CanRelease: true,
	})
	if first.ID == "" || first.ID != same.ID || first.ID == newer.ID {
		t.Fatalf("ids first=%q same=%q newer=%q", first.ID, same.ID, newer.ID)
	}
	if first.Token != "opaque-generation-a" || first.ServerID != "server-1" {
		t.Fatalf("internal reservation = %+v", first)
	}
}
