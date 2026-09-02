package main

import (
	"strings"
	"sync"
	"time"

	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
)

// viewFreshness remembers, per server, how old the projection actually is.
//
// The client had no way to answer that. clientview.Monitor reports a failed
// sync to OnError, the consumer wrote one warning line, and nothing reached
// the presentation - so a server whose view had not moved for ten days looked
// exactly like one synced a second ago. Measured on 2026-09-02: the desktop
// reported every repository on `manual` as online while its view was frozen at
// generation 14 from 23 August, because the only freshness the interface had
// was the GUI-to-daemon link.
//
// This is deliberately about the view lane and nothing else. The reservation
// emission already answers its own question honestly, per repository, and the
// same server can be fresh there and dead here: on that day `manual` refreshed
// reservations successfully while its view sync had been refused for ten days,
// because the two travel different SSH commands. One lane cannot speak for the
// other, and a single "is the server up" answer would have been wrong in both
// directions.
type viewFreshness struct {
	mu      sync.Mutex
	servers map[string]*serverViewFreshness
	now     func() time.Time
}

type serverViewFreshness struct {
	generation  int64
	generatedAt time.Time
	syncedAt    time.Time
	lastError   string
	failures    int
}

func newViewFreshness(now func() time.Time) *viewFreshness {
	if now == nil {
		now = time.Now
	}
	return &viewFreshness{servers: map[string]*serverViewFreshness{}, now: now}
}

func (f *viewFreshness) entry(serverID string) *serverViewFreshness {
	state, ok := f.servers[serverID]
	if !ok {
		state = &serverViewFreshness{}
		f.servers[serverID] = state
	}
	return state
}

// Synced records a view that arrived. It clears the error, because a view in
// hand is the strongest possible statement that the lane works right now.
func (f *viewFreshness) Synced(serverID string, view clientview.View) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.entry(serverID)
	state.generation = view.Generation
	state.generatedAt = view.GeneratedAt
	state.syncedAt = f.now()
	state.lastError = ""
	state.failures = 0
}

// Failed records a refusal without touching what we already hold. The previous
// view stays valid to display - it is simply older than the interface has been
// letting on, and saying how much older is the entire point.
func (f *viewFreshness) Failed(serverID string, err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.entry(serverID)
	state.failures++
	// Kept whole rather than classified. The reason a sync is refused is the
	// one thing a person needs here, and every attempt to summarise it so far
	// has produced a sentence that named the wrong cause.
	state.lastError = strings.TrimSpace(err.Error())
}

// Apply fills the freshness fields of one activation record.
func (f *viewFreshness) Apply(status contract.ActivationStatus) contract.ActivationStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, ok := f.servers[status.ServerID]
	if !ok {
		return status
	}
	status.ViewGeneration = state.generation
	if !state.generatedAt.IsZero() {
		status.ViewGeneratedAt = state.generatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !state.syncedAt.IsZero() {
		status.ViewSyncedAt = state.syncedAt.UTC().Format(time.RFC3339Nano)
	}
	status.ViewSyncError = state.lastError
	status.ViewSyncFailures = state.failures
	return status
}
