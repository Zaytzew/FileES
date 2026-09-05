package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationclient"
)

type namedStateFetcher struct {
	fixedReservationFetcher
	server reservationv1.Result
	err    error
	calls  int
}

func (f *namedStateFetcher) FetchServerState(context.Context) (reservationv1.Result, error) {
	f.calls++
	return f.server, f.err
}

func TestServerNameBrokerWinsWithoutReposAndSurvivesOldViewAndRestart(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache", "view.json")
	profile := clientprofile.Profile{ServerID: "spot", DisplayName: "spot", CachePath: cache, Address: "spot.example.net:2223", SSHPort: 2223}
	before := profile
	now := time.Now()
	f := &namedStateFetcher{server: reservationv1.Result{Schema: reservationv1.StateSchema, ServerID: "spot", ServerDisplayName: "40rs:filees", ViewGeneration: 26, ViewGeneratedAt: &now}}
	c := newReservationProjectionCoordinator(t.Context(), nil)
	defer c.Close()
	c.profiles["spot"] = profile
	c.views["spot"] = clientview.View{Generation: 26, ServerDisplayName: "spot"}
	c.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return f, nil }
	name := ""
	c.onServerDisplayName = func(id, label string) {
		if id != "spot" {
			t.Fatal("identity changed")
		}
		name = label
	}
	c.refresh(t.Context(), "spot")
	if f.calls != 1 || name != "40rs:filees" {
		t.Fatalf("broker not used without repos: %q calls=%d", name, f.calls)
	}
	path := reservationclient.ServerStatePath(cache)
	raw, _ := os.ReadFile(path)
	c.refresh(t.Context(), "spot")
	again, _ := os.ReadFile(path)
	if string(raw) != string(again) {
		t.Fatal("unchanged label rewrote mirror")
	}
	if c.profiles["spot"] != before {
		t.Fatal("name changed invitation transport/profile")
	}
	f.err = errors.New("offline")
	c.refresh(t.Context(), "spot")
	restored, exists, err := reservationclient.LoadServerState(path, "spot")
	if err != nil || !exists || restored.ServerDisplayName != "40rs:filees" {
		t.Fatalf("restart/old view lost name: %+v %v", restored, err)
	}
	f.err = nil
	f.server.ServerID = "another-server"
	f.server.ServerDisplayName = "wrong"
	c.refresh(t.Context(), "spot")
	if name != "40rs:filees" {
		t.Fatal("foreign identity changed label")
	}
	f.server.ServerID = "spot"
	f.server.ServerDisplayName = "Nowa nazwa"
	c.refresh(t.Context(), "spot")
	if name != "Nowa nazwa" {
		t.Fatal("name depended on unchanged view generation")
	}
}
