package commit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Cache the server's commit date, not the time this client received it.
// Called only at startup or after a revision change, never on an idle tick.
func (s *Service) recordLastCommit(ctx context.Context, wc string, revision int64) {
	if revision <= 0 {
		return
	}
	logger, ok := s.Cli.(revisionLogger)
	if !ok {
		return
	}
	path := filepath.Join(wc, ".filees", "state", "last_commit.json")
	var previous struct {
		Revision int64  `json:"revision"`
		Date     string `json:"date"`
	}
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &previous) == nil && previous.Revision == revision {
		if _, err := time.Parse(time.RFC3339Nano, previous.Date); err == nil {
			return
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entries, err := logger.LogMessages(ctx, wc, revision, revision)
	if err != nil {
		return
	} // Unknown is safer than receipt time or filesystem mtime.
	for _, entry := range entries {
		if entry.Revision != revision {
			continue
		}
		date, err := time.Parse(time.RFC3339Nano, entry.Date)
		if err != nil || date.IsZero() || date.After(time.Now().Add(5*time.Minute)) {
			return
		}
		previous.Revision, previous.Date = revision, date.UTC().Format(time.RFC3339Nano)
		raw, err := json.Marshal(previous)
		if err != nil {
			return
		}
		if err := s.writeStateString(path, string(raw)); err != nil {
			s.Logger.Warnf("last commit metadata: %v", err)
		}
		return
	}
}
