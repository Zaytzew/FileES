// Package notifications translates ViewModel transitions into informational
// desktop notifications. It has no IPC or platform implementation knowledge.
package notifications

import (
	"fmt"
	"sync/atomic"
	"time"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
)

const ConnectionGrace = 45 * time.Second

// Policy remembers the last presentation snapshot. Observe must be called by
// one owner (the app OnChange callback).
type Policy struct {
	initialized        bool
	observedConnection bool
	previous           app.ViewModel
	suppressConnection atomic.Bool
	now                func() time.Time
	disconnectedAt     time.Time
	disconnectReported bool
}

// SetClock is intended for deterministic transition tests. Production uses
// time.Now and keeps Policy's zero value ready to use.
func (p *Policy) SetClock(now func() time.Time) { p.now = now }

func (p *Policy) currentTime() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// SuppressConnectionTransitions mutes the expected brief disconnect while
// FileES deliberately restarts its daemon. Observe remains safe to call from
// the app loop while this method is called by a tray-action goroutine.
func (p *Policy) SuppressConnectionTransitions() { p.suppressConnection.Store(true) }

// RestoreConnectionTransitions undoes a suppression when the requested
// restart was rejected before the GUI actually begins its own restart.
func (p *Policy) RestoreConnectionTransitions() { p.suppressConnection.Store(false) }

// Observe returns notifications for meaningful transitions. The first fresh
// daemon snapshot establishes a baseline and never replays cached errors as
// startup notifications.
func (p *Policy) Observe(next app.ViewModel) []platform.Notification {
	if !p.initialized {
		// A successful hello intentionally publishes Connected+Stale before the
		// full status/error snapshot arrives. Waiting for that fresh snapshot
		// keeps persisted NET-* errors and other history from looking new.
		if !next.Connected || next.Stale {
			return nil
		}
		p.initialized = true
		p.observedConnection = true
		p.previous = next
		return nil
	}
	previous := p.previous
	p.previous = next
	var result []platform.Notification
	if p.suppressConnection.Load() {
		p.disconnectedAt = time.Time{}
		p.disconnectReported = false
	} else if !next.Connected && p.observedConnection {
		if previous.Connected || p.disconnectedAt.IsZero() {
			p.disconnectedAt = p.currentTime()
			p.disconnectReported = false
		}
		if !p.disconnectReported && p.currentTime().Sub(p.disconnectedAt) >= ConnectionGrace {
			result = append(result, platform.Notification{ID: "daemon.disconnected", Group: "daemon.connection", Title: "Brak połączenia z FileES", Body: "GUI ponawia połączenie z daemonem", Urgency: platform.UrgencyNormal})
			p.disconnectReported = true
		}
	} else if next.Connected {
		// The daemon can legitimately need a moment longer than the tray
		// process after FileES is restarted. Treat that first handshake as
		// startup, not a connection recovery worthy of a desktop toast.
		if !p.observedConnection {
			p.observedConnection = true
		} else if p.disconnectReported {
			result = append(result, platform.Notification{ID: "daemon.connected", Group: "daemon.connection", Title: "FileES połączony", Body: "Połączenie z daemonem zostało przywrócone", Urgency: platform.UrgencyLow})
		}
		p.disconnectedAt = time.Time{}
		p.disconnectReported = false
	}
	oldRepos := make(map[string]app.RepoDisplayState, len(previous.Repos))
	for _, repo := range previous.Repos {
		oldRepos[repo.ID] = repo.DisplayState()
	}
	for _, repo := range next.Repos {
		if repo.DisplayState() == app.RepoDisplayAttention && oldRepos[repo.ID] != app.RepoDisplayAttention {
			result = append(result, platform.Notification{ID: "repo.attention." + repo.ID, Group: "repo.attention." + repo.ID, Title: "Repozytorium wymaga uwagi", Body: repo.ID, Urgency: platform.UrgencyCritical})
		}
	}
	oldErrors := make(map[string]struct{}, len(previous.Errors))
	for _, record := range previous.Errors {
		oldErrors[record.ID] = struct{}{}
	}
	for _, record := range next.Errors {
		if _, exists := oldErrors[record.ID]; exists {
			continue
		}
		result = append(result, platform.Notification{ID: "daemon.error." + record.ID, Group: "daemon.errors", Title: fmt.Sprintf("FileES — %s", record.Code), Body: record.Message, Urgency: errorUrgency(record.Severity)})
	}
	return result
}

func errorUrgency(severity string) platform.Urgency {
	if severity == "ERROR" || severity == "FATAL" {
		return platform.UrgencyCritical
	}
	return platform.UrgencyNormal
}
