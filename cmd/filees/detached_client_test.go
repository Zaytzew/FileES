package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/reposupervisor"
	reservationv1 "filees/pkg/reservation/v1"
)

// The server's refusal is precise; what was missing is that it is terminal. It
// entered the same branch as a dropped connection, which carries a retry
// policy, so on 2026-09-03 the daemon knocked once a minute for hours - 234
// identical warnings in one afternoon - while the interface reported the
// server as unavailable. It was working perfectly and following the owner's
// own instruction to detach that client.
func TestARevokedClientIsRecognisedAsTerminal(t *testing.T) {
	refusal := errors.New("reservation worker failed: Process exited with status 69: " +
		"filees-client-entry proof: proof does not match one live staged or active client")
	if !isDetachedClient(refusal) {
		t.Fatal("a revoked client must not be treated as a transient transport fault")
	}
	for _, transient := range []error{
		nil,
		errors.New("dial tcp: connection refused"),
		errors.New("svn: E170013: Unable to connect to a repository"),
	} {
		if isDetachedClient(transient) {
			t.Fatalf("%v is transient and must keep its retry policy", transient)
		}
	}
}

// Said once. Repeating a terminal fact every cycle is what buried it.
func TestTheDetachmentIsAnnouncedOnce(t *testing.T) {
	coordinator := &reservationProjectionCoordinator{}
	if !coordinator.markDetached("manual") {
		t.Fatal("the first time must be announced")
	}
	for i := 0; i < 5; i++ {
		if coordinator.markDetached("manual") {
			t.Fatal("a terminal fact must not be repeated every cycle")
		}
	}
	// A different server is a different fact and gets its own sentence.
	if !coordinator.markDetached("spot") {
		t.Fatal("each server is announced on its own")
	}
}

// Re-activating must heal silently and then be able to announce a second
// detachment, so the flag has to clear on the first success.
func TestReactivationClearsTheState(t *testing.T) {
	coordinator := &reservationProjectionCoordinator{}
	coordinator.markDetached("manual")
	coordinator.forgetDetached("manual")
	if !coordinator.markDetached("manual") {
		t.Fatal("after a successful refresh a later detachment must be announced again")
	}
}

type countingDetachedFetcher struct{ calls atomic.Int32 }

func (fetcher *countingDetachedFetcher) Fetch(context.Context, string) (reservationv1.Result, error) {
	fetcher.calls.Add(1)
	return reservationv1.Result{}, errors.New("filees-client-entry proof: proof does not match one live staged or active client")
}

func TestDetachedCredentialStopsPeriodicTransportUntilActivation(t *testing.T) {
	coordinator := newReservationProjectionCoordinator(t.Context(), nil)
	t.Cleanup(coordinator.Close)
	fetcher := &countingDetachedFetcher{}
	coordinator.newClient = func(clientprofile.Profile) (reservationFetcher, error) { return fetcher, nil }
	profile := clientprofile.Profile{ServerID: "manual", PollInterval: 10 * time.Millisecond}
	view := clientview.View{Repositories: []clientview.Repository{
		{RepoID: "one", State: "active"},
		{RepoID: "two", State: "active"},
	}}
	coordinator.UpdateProfile(profile)
	coordinator.UpdateView("manual", view)

	deadline := time.Now().Add(time.Second)
	for fetcher.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := fetcher.calls.Load(); got != 1 {
		t.Fatalf("one proof refusal must stop the whole server cycle, calls=%d", got)
	}
	coordinator.mu.RLock()
	for _, repoID := range []string{"one", "two"} {
		state := coordinator.results[reposupervisor.Key{ServerID: "manual", RepoID: repoID}]
		if !state.detached || state.offline {
			coordinator.mu.RUnlock()
			t.Fatalf("repository %s did not inherit the server detachment: %+v", repoID, state)
		}
	}
	coordinator.mu.RUnlock()
	time.Sleep(60 * time.Millisecond)
	if got := fetcher.calls.Load(); got != 1 {
		t.Fatalf("periodic ticks reopened transport for a detached credential, calls=%d", got)
	}

	if !coordinator.Resume("manual") {
		t.Fatal("an explicit activation must resume the paused transport")
	}
	coordinator.Schedule("manual")
	deadline = time.Now().Add(time.Second)
	for fetcher.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := fetcher.calls.Load(); got != 2 {
		t.Fatalf("replacement activation did not get one validation attempt, calls=%d", got)
	}
}

// Offline and detached are different facts and must stay different fields.
// Offline says the server could not be reached; detached says it was reached
// and refused us. They ask for opposite responses - wait for the network,
// versus activate this client again - so folding one into the other sends the
// reader to fix something that is not broken.
func TestDetachedIsNotFiledAsOffline(t *testing.T) {
	coordinator := &reservationProjectionCoordinator{
		results: map[reposupervisor.Key]cachedReservationResult{},
	}
	key := reposupervisor.Key{ServerID: "manual", RepoID: "bbbddef3"}

	refusal := errors.New("filees-client-entry proof: proof does not match one live staged or active client")
	cached := coordinator.results[key]
	if isDetachedClient(refusal) {
		cached.detached = true
	} else {
		cached.offline = true
	}
	coordinator.results[key] = cached

	if got := coordinator.results[key]; !got.detached || got.offline {
		t.Fatalf("a revoked client is detached and not offline: %+v", got)
	}

	// A genuine transport failure keeps the old meaning.
	unreachable := errors.New("dial tcp 10.0.1.1:2222: connect: connection refused")
	other := cachedReservationResult{}
	if isDetachedClient(unreachable) {
		other.detached = true
	} else {
		other.offline = true
	}
	if !other.offline || other.detached {
		t.Fatalf("an unreachable server is offline and not detached: %+v", other)
	}
}

// The wrapper must satisfy clientview.Cleaner, or the release of a locked
// service working copy silently never runs.
//
// It did not, and the r786 fix went to production without ever executing:
// serviceProjectionUpdater exposed only Update, the type assertion inside
// clientview.Sync failed, and the lock reappeared every cycle while the
// interface reported the server as unreachable. An optional interface is only
// optional for implementations that genuinely cannot satisfy it.
func TestTheServiceUpdaterCanReleaseALock(t *testing.T) {
	var updater any = serviceProjectionUpdater{}
	if _, ok := updater.(clientview.Cleaner); !ok {
		t.Fatal("serviceProjectionUpdater must satisfy clientview.Cleaner")
	}
	if _, ok := updater.(clientview.Updater); !ok {
		t.Fatal("serviceProjectionUpdater must still satisfy clientview.Updater")
	}
}
