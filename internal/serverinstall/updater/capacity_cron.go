package updater

import (
	"context"
	"filees/internal/serverinstall/cronjob"
	"filees/internal/serverinstall/manifest"
	"path/filepath"
)

// The signed example file declares that this release implements the scheduler.
// Old releases without it must not acquire a job calling an unknown command.
func (r *Runner) hasCapacityMonitor(m *manifest.Manifest) bool {
	for _, f := range m.Files {
		if manifest.ResolveTarget(r.dirs(), f.Target) == filepath.Join(r.Config.SysconfDir, "capacity-alerts.example.json") {
			return true
		}
	}
	return false
}

var prepareCapacityCron = cronjob.Prepare

func (r *Runner) capacityCron(ctx context.Context, m *manifest.Manifest, first bool) (*cronjob.Plan, error) {
	if !r.hasCapacityMonitor(m) {
		return nil, nil
	}
	return prepareCapacityCron(ctx, filepath.Join(r.Config.SbinDir, "filees-admin"), filepath.Join(r.Config.SysconfDir, "server.json"), first)
}
