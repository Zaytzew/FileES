// Package notifications translates ViewModel transitions into informational
// desktop notifications. It has no IPC or platform implementation knowledge.
package notifications

import (
	"fmt"

	"filees/internal/gui/app"
	"filees/internal/gui/platform"
)

// Policy remembers the last presentation snapshot. Observe must be called by
// one owner (the app OnChange callback).
type Policy struct {
	initialized bool
	previous    app.ViewModel
}

// Observe returns notifications for meaningful transitions. The first
// snapshot establishes a baseline and never produces startup noise.
func (p *Policy) Observe(next app.ViewModel) []platform.Notification {
	if !p.initialized {
		p.initialized = true
		p.previous = next
		return nil
	}
	previous := p.previous
	p.previous = next
	var result []platform.Notification
	if previous.Connected != next.Connected {
		if next.Connected {
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
