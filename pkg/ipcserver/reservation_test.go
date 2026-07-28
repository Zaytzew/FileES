package ipcserver

import (
	"context"
	"errors"
	"testing"

	contract "filees/pkg/contract/v1"
)

type ownerLabelResolverStub struct{ labels map[string]string }

func (stub ownerLabelResolverStub) Resolve(_ context.Context, _ string, _ []string) (map[string]string, error) {
	return stub.labels, nil
}

func TestReservationListAggregatesOneServerInWorkingCopyOrder(t *testing.T) {
	server := New("unused")
	first := server.RegisterRepoAccess("first", "svn+ssh://host/first", "/work/a", "office", contract.AccessReadWrite)
	second := server.RegisterRepoAccess("second", "svn+ssh://host/second", "/work/b", "office", contract.AccessReadOnly)
	other := server.RegisterRepoAccess("other", "svn+ssh://host/other", "/work/other", "home", contract.AccessReadWrite)
	first.SetReservationFuncs(func(context.Context) ([]contract.Reservation, error) {
		return []contract.Reservation{{RepoID: "first", WorkingCopy: "/work/a", Path: "z.txt", Token: "z"}, {RepoID: "first", WorkingCopy: "/work/a", Path: "a.txt", Token: "a"}}, nil
	}, nil)
	second.SetReservationFuncs(func(context.Context) ([]contract.Reservation, error) {
		return []contract.Reservation{{RepoID: "second", WorkingCopy: "/work/b", Path: "shared.txt", Token: "b", CanRelease: false}}, nil
	}, nil)
	other.SetReservationFuncs(func(context.Context) ([]contract.Reservation, error) {
		return []contract.Reservation{{RepoID: "other", WorkingCopy: "/work/other", Path: "secret.txt", Token: "other"}}, nil
	}, nil)

	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	if response.Status != contract.StatusOK {
		t.Fatalf("list response=%+v", response.Error)
	}
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	if result.ServerID != "office" || len(result.Reservations) != 3 {
		t.Fatalf("result=%+v", result)
	}
	if result.Reservations[0].Path != "a.txt" || result.Reservations[1].Path != "z.txt" || result.Reservations[2].RepoID != "second" {
		t.Fatalf("reservations were not sorted/grouped deterministically: %+v", result.Reservations)
	}
}

func TestReservationListExposesOnlyResolvedOwnerLabel(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	ownerID := "11111111-1111-4111-8111-111111111111"
	repo.SetReservationFuncs(func(context.Context) ([]contract.Reservation, error) {
		return []contract.Reservation{{RepoID: "docs", WorkingCopy: "/work/docs", Path: "a.txt", Token: "token", OwnerID: ownerID}}, nil
	}, nil)
	server.SetOwnerLabelResolver(ownerLabelResolverStub{labels: map[string]string{ownerID: "anna"}})
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	if response.Status != contract.StatusOK {
		t.Fatalf("list response=%+v", response.Error)
	}
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	if len(result.Reservations) != 1 || result.Reservations[0].OwnerLabel != "anna" || result.Reservations[0].OwnerID != "" {
		t.Fatalf("reservation presentation=%+v", result.Reservations)
	}
}

func TestReservationReleaseIsServerScopedAndForwardsTokenAndRiskConfirmation(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	var gotPath, gotToken string
	var gotRisk bool
	repo.SetReservationFuncs(nil, func(_ context.Context, path, token string, risk bool) error {
		gotPath, gotToken, gotRisk = path, token, risk
		return nil
	})
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationRelease, contract.RepoReservationReleasePayload{ServerID: "office", RepoID: "docs", Path: "docs/a.txt", ExpectedToken: "opaque", ConfirmRisk: true}))
	if response.Status != contract.StatusOK {
		t.Fatalf("release response=%+v", response.Error)
	}
	if gotPath != "docs/a.txt" || gotToken != "opaque" || !gotRisk {
		t.Fatalf("release callback args path=%q token=%q risk=%v", gotPath, gotToken, gotRisk)
	}

	response = server.dispatch(lifecycleRequest(contract.CmdRepoReservationRelease, contract.RepoReservationReleasePayload{ServerID: "home", RepoID: "docs", Path: "docs/a.txt", ExpectedToken: "opaque"}))
	if response.Status == contract.StatusOK {
		t.Fatal("cross-server reservation release accepted")
	}
}

func TestReservationReleaseRejectsTraversalBeforeCallback(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	called := false
	repo.SetReservationFuncs(nil, func(context.Context, string, string, bool) error { called = true; return errors.New("unexpected") })
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationRelease, contract.RepoReservationReleasePayload{ServerID: "office", RepoID: "docs", Path: "../elsewhere", ExpectedToken: "opaque"}))
	if response.Status == contract.StatusOK || called {
		t.Fatalf("traversal response=%+v called=%v", response, called)
	}
}
