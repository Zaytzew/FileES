package main

import (
	"strings"
	"testing"

	guiapp "filees/internal/gui/app"
	contract "filees/pkg/contract/v1"
)

func TestProjectWailsTrayTracksConnectionRepositoriesAndLocks(t *testing.T) {
	projection := projectWailsTray(Snapshot{
		Connected: true, IconState: string(guiapp.IconActive),
		Capabilities: []string{contract.CapSystemRestart, contract.CapSystemShutdown},
		Servers:      []ServerProjection{{ID: "server", ReservationsKnown: true}},
		Repositories: []RepoProjection{{ID: "one"}, {ID: "two"}},
		Reservations: []ReservationProjection{{ID: "lock"}},
	})
	if projection.Icon != guiapp.IconActive || projection.Status != "Połączono · 2 repozytoria · 1 blokada" || projection.Tooltip == "" {
		t.Fatalf("projection = %+v", projection)
	}
	if !projection.CanRestart || !projection.CanShutdown {
		t.Fatalf("lifecycle unavailable: %+v", projection)
	}

	disconnected := projectWailsTray(Snapshot{})
	if disconnected.Icon != guiapp.IconDisconnected || disconnected.Status != "Rozłączono · 0 repozytoriów · 0 blokad (stan niezweryfikowany)" {
		t.Fatalf("disconnected = %+v", disconnected)
	}

	unknown := projectWailsTray(Snapshot{Connected: true, Servers: []ServerProjection{{ID: "server"}}})
	if unknown.Status != "Połączono · 0 repozytoriów · 0+? blokad (1 bez emisji)" {
		t.Fatalf("unknown reservations = %+v", unknown)
	}

	partial := projectWailsTray(Snapshot{
		Connected:    true,
		Servers:      []ServerProjection{{ID: "spot", ReservationsKnown: true}, {ID: "cloud"}},
		Reservations: []ReservationProjection{{ID: "lock"}},
	})
	if partial.Status != "Połączono · 0 repozytoriów · 1+? blokad (1 bez emisji)" {
		t.Fatalf("partial reservations = %+v", partial)
	}

	stale := projectWailsTray(Snapshot{
		Connected: true, Stale: true,
		Capabilities: []string{contract.CapSystemRestart, contract.CapSystemShutdown},
	})
	if stale.CanRestart || stale.CanShutdown {
		t.Fatalf("stale lifecycle available: %+v", stale)
	}
}

func TestRepositoryNoun(t *testing.T) {
	cases := map[int]string{0: "repozytoriów", 1: "repozytorium", 2: "repozytoria", 4: "repozytoria", 5: "repozytoriów", 12: "repozytoriów", 14: "repozytoriów", 22: "repozytoria", 25: "repozytoriów"}
	for count, want := range cases {
		if got := repositoryNoun(count); got != want {
			t.Errorf("repositoryNoun(%d)=%q, want %q", count, got, want)
		}
	}
}

func TestProjectWailsTrayMakesUnreadAnnouncementsDominant(t *testing.T) {
	projection := projectWailsTray(Snapshot{
		Connected: true, IconState: string(guiapp.IconShout),
		Servers: []ServerProjection{{ID: "server", ReservationsKnown: true}},
		Notices: []NoticeProjection{{ID: "one"}, {ID: "two"}, {ID: "old", Acked: true}},
	})
	if projection.Icon != guiapp.IconShout || projection.Unread != 2 || !strings.HasPrefix(projection.Status, "2 ogłoszenia do przejrzenia · ") {
		t.Fatalf("projection=%+v", projection)
	}
}

func TestAnnouncementAlertPolicySuppressesStartupAndNotifiesOnlyNewUnread(t *testing.T) {
	var policy announcementAlertPolicy
	baseline := Snapshot{Connected: true, Repositories: []RepoProjection{{ID: "docs", DisplayName: "Dokumenty"}}, Notices: []NoticeProjection{{ID: "old", RepoID: "docs", Title: "stare"}}}
	if got := policy.Observe(baseline); len(got) != 0 {
		t.Fatalf("startup replay=%#v", got)
	}
	withNew := baseline
	withNew.Notices = append(withNew.Notices,
		NoticeProjection{ID: "new", RepoID: "docs", Title: "pilne"},
		NoticeProjection{ID: "already-read", RepoID: "docs", Title: "przeczytane", Acked: true},
	)
	got := policy.Observe(withNew)
	if len(got) != 1 || got[0].Title != "Nowe ogłoszenie" || got[0].Body != "Dokumenty — pilne" {
		t.Fatalf("notifications=%#v", got)
	}
	if replay := policy.Observe(withNew); len(replay) != 0 {
		t.Fatalf("duplicate notifications=%#v", replay)
	}
	stale := withNew
	stale.Stale = true
	stale.Notices = append(stale.Notices, NoticeProjection{ID: "while-stale", Title: "nie teraz"})
	if got := policy.Observe(stale); len(got) != 0 {
		t.Fatalf("stale notifications=%#v", got)
	}
	stale.Stale = false
	got = policy.Observe(stale)
	if len(got) != 1 || got[0].Body != "nie teraz" {
		t.Fatalf("fresh notification after stale=%#v", got)
	}
}
