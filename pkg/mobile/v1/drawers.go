package v1

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Validate mirrors cmd/filees-gui-wails/frontend/drawer-layout.js's
// parseDrawers exactly, including its limits, so a document the GUI itself
// would refuse to load never reaches the phone as if it were fine. It
// accepts a missing document: nil slices and a nil assignment map are the
// normal empty projection (most realms never create a drawer). A non-empty
// frame must name each drawer once and point every assignment at one of
// those ids.
func (r ListDrawersResult) Validate() error {
	if r.Version != "" {
		if err := validateUUID("version", r.Version); err != nil {
			return err
		}
	}
	if len(r.Drawers) > 128 {
		return fmt.Errorf("drawers frame is too large")
	}
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
	if len(r.Assignments) > 2048 {
		return fmt.Errorf("drawers frame is too large")
	}
	for repo, id := range r.Assignments {
		// drawer-layout.js's idOK applies to repo keys too, the same charset
		// as a drawer id - not the wider requireRepoID a real repo_id
		// otherwise allows elsewhere on this wire. A key the GUI's own
		// assign action would have refused must not validate here either.
		if !drawerID(repo) {
			return fmt.Errorf("assignments key %q is invalid", repo)
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
