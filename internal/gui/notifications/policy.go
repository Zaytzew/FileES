// Package notifications translates ViewModel transitions into informational
// desktop notifications. It has no IPC or platform implementation knowledge.
package notifications

import (
	"fmt"
	"sync/atomic"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
)

// Policy remembers the last presentation snapshot. Observe must be called by
// one owner (the app OnChange callback).
type Policy struct {
	initialized        bool
	observedConnection bool
	previous           app.ViewModel
	suppressConnection atomic.Bool
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
	if previous.Connected != next.Connected && !p.suppressConnection.Load() {
		// The daemon can legitimately need a moment longer than the tray
		// process after FileES is restarted. Treat that first handshake as
		// startup, not a connection recovery worthy of a desktop toast.
		if !p.observedConnection {
			p.observedConnection = next.Connected
		} else if next.Connected {
			result = append(result, platform.Notification{ID: "daemon.connected", Group: "daemon.connection", Title: "FileES połączony", Body: "Połączenie z daemonem zostało przywrócone", Urgency: platform.UrgencyLow})
		} else {
			result = append(result, platform.Notification{ID: "daemon.disconnected", Group: "daemon.connection", Title: "Brak połączenia z FileES", Body: "GUI ponawia połączenie z daemonem", Urgency: platform.UrgencyNormal})
		}
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
