package storage

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Budget describes additional required capacity, not existing usage or a quota.
type Budget struct {
	Path     string
	Required int64
}

// VolumeBudget is an advisory snapshot. Device identifies a filesystem, not a
// physical disk: two partitions have independent capacity. Writers must still
// handle ENOSPC after this check.
type VolumeBudget struct {
	Device    string
	Paths     []string
	Available int64
	Required  int64
	Reserve   int64
}

type capacityProbe func(string) (device string, available int64, err error)

// InspectBudgets combines all consumers of the same filesystem before checking
// space. Missing directories are measured at their nearest existing ancestor.
// It creates nothing and does not change any configuration.
func InspectBudgets(requests []Budget) ([]VolumeBudget, error) {
	return inspectBudgets(requests, probeCapacity)
}

func inspectBudgets(requests []Budget, probe capacityProbe) ([]VolumeBudget, error) {
	byDevice := map[string]*VolumeBudget{}
	for _, request := range requests {
		if !filepath.IsAbs(request.Path) || request.Required < 0 {
			return nil, errors.New("storage budget needs an absolute path and nonnegative size")
		}
		ancestor, resolved, err := budgetPath(request.Path)
		if err != nil {
			return nil, err
		}
		device, available, err := probe(ancestor)
		if err != nil {
			return nil, fmt.Errorf("inspect storage %s: %w", request.Path, err)
		}
		if device == "" || available < 0 {
			return nil, errors.New("invalid filesystem capacity observation")
		}
		volume := byDevice[device]
		if volume == nil {
			volume = &VolumeBudget{Device: device, Available: available, Reserve: 16 << 20}
			byDevice[device] = volume
		}
		// Probes are not simultaneous; use the lowest observed free capacity.
		if available < volume.Available {
			volume.Available = available
		}
		if volume.Required > math.MaxInt64-request.Required {
			return nil, errors.New("storage budget overflow")
		}
		volume.Required += request.Required
		volume.Paths = append(volume.Paths, resolved)
	}
	result := make([]VolumeBudget, 0, len(byDevice))
	for _, volume := range byDevice {
		sort.Strings(volume.Paths)
		result = append(result, *volume)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Device < result[j].Device })
	return result, nil
}

// Check reports insufficiency after the caller has had a chance to display the
// evidence. The reserve matches the writer's existing admission margin.
func (v VolumeBudget) Check() error {
	return CheckSpace(v.Available, v.Required)
}

func budgetPath(path string) (ancestor, resolved string, err error) {
	probe, suffix := filepath.Clean(path), ""
	for {
		info, statErr := os.Stat(probe)
		if statErr == nil {
			if !info.IsDir() {
				return "", "", fmt.Errorf("storage ancestor is not a directory: %s", probe)
			}
			real, resolveErr := filepath.EvalSymlinks(probe)
			if resolveErr != nil {
				return "", "", resolveErr
			}
			return real, filepath.Join(real, suffix), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", "", statErr
		}
		// A dangling symlink is not a missing directory to be provisioned.
		if _, linkErr := os.Lstat(probe); linkErr == nil {
			return "", "", fmt.Errorf("unresolved storage path: %s", probe)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", "", statErr
		}
		suffix = filepath.Join(filepath.Base(probe), suffix)
		probe = parent
	}
}
