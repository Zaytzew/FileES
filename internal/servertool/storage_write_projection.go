package servertool

import (
	"os"
	"path/filepath"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/storagewatch"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
)

// The forced-command dispatcher locks unveil before exec. Optional monitoring
// must not prevent legacy state requests when its files are absent or invalid.
func storageWriteReadPaths(configPath string) []obsandbox.Path {
	path := filepath.Join(filepath.Dir(configPath), "capacity-alerts.json")
	settings, err := loadCapacityConfig(path)
	if err != nil {
		return nil
	}
	paths := []obsandbox.Path{{Label: "capacity-settings", Name: path, Perms: "r"}}
	state := filepath.Join(settings.StateDir, "capacity.json")
	if info, err := os.Stat(state); err == nil && info.Mode().IsRegular() {
		paths = append(paths, obsandbox.Path{Label: "capacity-observation", Name: state, Perms: "r"})
	}
	return paths
}

// Called only after the existing broker has authorized an active repository.
// No global counters, paths, device IDs, incident text or contacts cross the wire.
func attachStorageWrite(configPath string, c serverconfig.Config, req reservationv1.Request, result *reservationv1.Result) {
	if !req.IncludeStorageWrite || result.RepositoryState != "active" {
		return
	}
	result.StorageWrite = projectStorageWrite(configPath, c, req.RepoID, time.Now(), storagewatch.Native{}.Device)
}

func projectStorageWrite(configPath string, c serverconfig.Config, repoID string, now time.Time, device func(string) (uint64, error)) *reservationv1.StorageWrite {
	unknown := &reservationv1.StorageWrite{State: "unknown"}
	settings, err := loadCapacityConfig(filepath.Join(filepath.Dir(configPath), "capacity-alerts.json"))
	if err != nil {
		return unknown
	}
	state, err := storagewatch.Load(filepath.Join(settings.StateDir, "capacity.json"), settings.Realm, settings.Email)
	if err != nil {
		return unknown
	}
	// A normal desktop publication writes FSFS and its server-side authority
	// records. Do not gate it on unrelated public-download or mobile buffers.
	paths := []string{filepath.Join(c.Repositories.Root, repoID), c.Repositories.ResultsRoot,
		c.Activation.ServiceRepository, c.Activation.ServiceWorkingCopy, c.Activation.Root}
	var devices []uint64
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return unknown
		}
		id, err := device(path)
		if err != nil {
			return unknown
		}
		devices = append(devices, id)
	}
	status, measured, remaining := state.Observation.WriteState(devices, now)
	seconds := int(remaining / time.Second) // round down, never extend lifetime
	if status == "unknown" || seconds < 1 {
		return unknown
	}
	return &reservationv1.StorageWrite{State: status, MeasuredAt: &measured, ValidForSeconds: seconds}
}
