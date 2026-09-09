package watcher

import (
	"encoding/json"
	"os"
)

// checkpointIdentities enriches only unchanged entries already present in the
// durable publication baseline. Saving the whole observed scan here would lose
// uncommitted changes after a crash. Called with scanMu held, like publication
// acknowledgement; it never advances content, paths or deletion observations.
func (s *Scanner) checkpointIdentities(observed index) error {
	if !s.requireRenameIdentity || s.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return err
	}
	var entries []diskEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	changed := false
	for i := range entries {
		e := &entries[i]
		m, ok := observed[e.Path]
		if !ok || e.Identity != "" || m.Identity == "" || e.IsDir || m.IsDir ||
			e.MD5 == "" || e.MD5 != m.MD5 || e.Size != m.Size || e.Mtime != m.MtimeSec {
			continue
		}
		e.Identity = m.Identity
		changed = true
	}
	if !changed {
		return nil
	}
	return s.writeJSON(s.statePath, entries)
}
