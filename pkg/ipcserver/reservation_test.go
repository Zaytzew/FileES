package ipcserver

import (
	"context"
	"errors"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
)

type ownerLabelResolverStub struct{ labels map[string]string }

func (stub ownerLabelResolverStub) Resolve(_ context.Context, _ string, _ []string) (map[string]string, error) {
	return stub.labels, nil
}

func freshSnapshot(rows ...contract.Reservation) ReservationSnapshot {
	return ReservationSnapshot{Reservations: rows}
}

func sourceFor(t *testing.T, result contract.RepoReservationListResult, repoID string) contract.ReservationSource {
	t.Helper()
	for _, source := range result.Sources {
		if source.RepoID == repoID {
			return source
		}
	}
	t.Fatalf("no source recorded for repo %q in %+v", repoID, result.Sources)
	return contract.ReservationSource{}
}

func TestReservationListAggregatesOneServerInWorkingCopyOrder(t *testing.T) {
	server := New("unused")
	first := server.RegisterRepoAccess("first", "svn+ssh://host/first", "/work/a", "office", contract.AccessReadWrite)
	second := server.RegisterRepoAccess("second", "svn+ssh://host/second", "/work/b", "office", contract.AccessReadOnly)
	other := server.RegisterRepoAccess("other", "svn+ssh://host/other", "/work/other", "home", contract.AccessReadWrite)
	first.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return freshSnapshot(contract.Reservation{RepoID: "first", WorkingCopy: "/work/a", Path: "z.txt", Token: "z"}, contract.Reservation{RepoID: "first", WorkingCopy: "/work/a", Path: "a.txt", Token: "a"}), nil
	}, nil)
	second.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return freshSnapshot(contract.Reservation{RepoID: "second", WorkingCopy: "/work/b", Path: "shared.txt", Token: "b", CanRelease: false}), nil
	}, nil)
	other.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return freshSnapshot(contract.Reservation{RepoID: "other", WorkingCopy: "/work/other", Path: "secret.txt", Token: "other"}), nil
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
	if len(result.Sources) != 2 {
		t.Fatalf("expected one source per contributing repo (first, second), got %+v", result.Sources)
	}
	if sourceFor(t, result, "first").State != contract.ReservationSourceFresh || sourceFor(t, result, "second").State != contract.ReservationSourceFresh {
		t.Fatalf("expected both repos fresh: %+v", result.Sources)
	}
}

func TestReservationListExposesOnlyResolvedOwnerLabel(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	ownerID := "11111111-1111-4111-8111-111111111111"
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return freshSnapshot(contract.Reservation{RepoID: "docs", WorkingCopy: "/work/docs", Path: "a.txt", Token: "token", OwnerID: ownerID}), nil
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

func TestReservationListFreshSingleRepoReportsFreshnessMetadata(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	asOf := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{
			Reservations: []contract.Reservation{{RepoID: "docs", WorkingCopy: "/work/docs", Path: "a.txt", Token: "a"}},
			AsOf:         asOf,
			Generation:   "7",
		}, nil
	}, nil)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	source := sourceFor(t, result, "docs")
	if source.State != contract.ReservationSourceFresh || source.Generation != "7" || !source.AsOf.Equal(asOf) {
		t.Fatalf("expected relayed freshness metadata, got %+v", source)
	}
}

func TestReservationListRelaysStaleClassificationFromSource(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	asOf := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{
			Reservations: []contract.Reservation{{RepoID: "docs", WorkingCopy: "/work/docs", Path: "a.txt", Token: "a"}},
			Stale:        true,
			AsOf:         asOf,
			Generation:   "6",
		}, nil
	}, nil)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	if response.Status != contract.StatusOK {
		t.Fatalf("a stale-but-known snapshot must still be a successful IPC response: %+v", response.Error)
	}
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	source := sourceFor(t, result, "docs")
	if source.State != contract.ReservationSourceStale || source.Generation != "6" || !source.AsOf.Equal(asOf) || len(result.Reservations) != 1 {
		t.Fatalf("expected relayed stale snapshot, got source=%+v result=%+v", source, result)
	}
}

func TestReservationListUnknownSourceReportsAsUnknownNotFalseZero(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{Unknown: true}, nil
	}, nil)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	if response.Status != contract.StatusOK {
		t.Fatalf("an unknown repo is still a successful IPC call carrying an unknown source, not an IPC error: %+v", response.Error)
	}
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	if len(result.Reservations) != 0 {
		t.Fatalf("unknown repo must contribute zero reservation rows: %+v", result.Reservations)
	}
	source := sourceFor(t, result, "docs")
	if source.State != contract.ReservationSourceUnknown || !source.AsOf.IsZero() || source.Generation != "" {
		t.Fatalf("expected a plain unknown source with no faked freshness, got %+v", source)
	}
}

func TestReservationListUnwiredRepoIsUnknownSource(t *testing.T) {
	server := New("unused")
	server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	// Deliberately never call SetReservationFuncs — this is the exact
	// window a repo instance passes through on every restart.
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	if sourceFor(t, result, "docs").State != contract.ReservationSourceUnknown {
		t.Fatalf("expected an unwired repo to report unknown: %+v", result.Sources)
	}
}

func TestReservationListHardErrorFromSourceBecomesUnknownNotAWholeFailure(t *testing.T) {
	server := New("unused")
	repo := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "office", contract.AccessReadWrite)
	repo.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{}, errors.New("reservation-v1 protocol: malformed result")
	}, nil)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	if response.Status != contract.StatusOK {
		t.Fatalf("a transport error for one repo must not fail the whole IPC call: %+v", response.Error)
	}
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	if sourceFor(t, result, "docs").State != contract.ReservationSourceUnknown {
		t.Fatalf("expected the failing repo to report unknown: %+v", result.Sources)
	}
}

func TestReservationListOnePartiallyUnknownRepoDoesNotHideAnothersFreshData(t *testing.T) {
	// This is the exact bug flagged in review: a server with one fresh and
	// one unknown repository must never look "fully known" to the caller.
	server := New("unused")
	broken := server.RegisterRepoAccess("broken", "svn+ssh://host/broken", "/work/broken", "office", contract.AccessReadWrite)
	healthy := server.RegisterRepoAccess("healthy", "svn+ssh://host/healthy", "/work/healthy", "office", contract.AccessReadWrite)
	broken.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{Unknown: true}, nil
	}, nil)
	healthy.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return freshSnapshot(contract.Reservation{RepoID: "healthy", WorkingCopy: "/work/healthy", Path: "a.txt", Token: "a"}), nil
	}, nil)
	response := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "office"}))
	var result contract.RepoReservationListResult
	decodeIPCResult(t, response, &result)
	if len(result.Sources) != 2 {
		t.Fatalf("expected a source entry for both repos, got %+v", result.Sources)
	}
	if sourceFor(t, result, "broken").State != contract.ReservationSourceUnknown {
		t.Fatalf("broken repo must be reported unknown: %+v", result.Sources)
	}
	if sourceFor(t, result, "healthy").State != contract.ReservationSourceFresh {
		t.Fatalf("healthy repo must still be reported fresh: %+v", result.Sources)
	}
	if len(result.Reservations) != 1 || result.Reservations[0].RepoID != "healthy" {
		t.Fatalf("healthy repo's reservation must still be in the flat list: %+v", result.Reservations)
	}
}

func TestReservationListOneServerFailureDoesNotTouchAnother(t *testing.T) {
	server := New("unused")
	cloud := server.RegisterRepoAccess("aktualne", "svn+ssh://host/aktualne", "/work/aktualne", "cloud", contract.AccessReadWrite)
	spot := server.RegisterRepoAccess("docs", "svn+ssh://host/docs", "/work/docs", "spot", contract.AccessReadWrite)
	cloud.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return ReservationSnapshot{Unknown: true}, nil
	}, nil)
	spot.SetReservationFuncs(func(context.Context) (ReservationSnapshot, error) {
		return freshSnapshot(contract.Reservation{RepoID: "docs", WorkingCopy: "/work/docs", Path: "a.txt", Token: "a"}), nil
	}, nil)

	cloudResp := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "cloud"}))
	var cloudResult contract.RepoReservationListResult
	decodeIPCResult(t, cloudResp, &cloudResult)
	if sourceFor(t, cloudResult, "aktualne").State != contract.ReservationSourceUnknown {
		t.Fatalf("cloud must report unknown: %+v", cloudResult.Sources)
	}
	spotResp := server.dispatch(lifecycleRequest(contract.CmdRepoReservationList, contract.RepoReservationListPayload{ServerID: "spot"}))
	var spotResult contract.RepoReservationListResult
	decodeIPCResult(t, spotResp, &spotResult)
	if spotResp.Status != contract.StatusOK || sourceFor(t, spotResult, "docs").State != contract.ReservationSourceFresh || len(spotResult.Reservations) != 1 {
		t.Fatalf("spot must be unaffected by cloud's failure: status=%v result=%+v", spotResp.Status, spotResult)
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
