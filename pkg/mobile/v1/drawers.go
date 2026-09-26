package v1

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Validate accepts a missing document: nil slices and a nil assignment map
// are the normal empty projection. A non-empty frame must name each drawer
// once and point every assignment at one of those ids.
func (r ListDrawersResult) Validate() error {
	ids := make(map[string]struct{}, len(r.Drawers))
	for i, drawer := range r.Drawers {
		if !drawerID(drawer.ID) || strings.TrimSpace(drawer.Name) == "" || utf8.RuneCountInString(drawer.Name) > 80 {
			return fmt.Errorf("drawers[%d] is invalid", i)
		}
		if _, dup := ids[drawer.ID]; dup {
			return fmt.Errorf("drawers[%d] is duplicated", i)
		}
		ids[drawer.ID] = struct{}{}
	}
	if len(r.Assignments) > 2048 || len(r.Drawers) > 128 {
		return fmt.Errorf("drawers frame is too large")
	}
	for repo, id := range r.Assignments {
		if err := requireRepoID(repo); err != nil {
			return fmt.Errorf("assignments key: %w", err)
		}
		if _, ok := ids[id]; !ok {
			return fmt.Errorf("assignments[%s] points at an unknown drawer", repo)
		}
	}
	return nil
}

func drawerID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
