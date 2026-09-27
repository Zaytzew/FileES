// Package storagewatch samples filesystem metadata and changes capacity incidents.
// It never scans file trees or reserves space for writes.
package storagewatch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/alertchannel"
)

type Path struct {
	Label string `json:"label"`
	Name  string `json:"path"`
}
type Sample struct{ Total, Available, Files, FreeFiles uint64 }
type Volume struct {
	Device uint64
	Labels []string
	Sample Sample
}
type Reader interface {
	Device(string) (uint64, error)
	Sample(string) (Sample, error)
}
type Native struct{}
type Policy struct {
	WarningPercent    float64 `json:"warning_percent"`
	CriticalPercent   float64 `json:"critical_percent"`
	HysteresisPercent float64 `json:"hysteresis_percent"`
	WarningBytes      uint64  `json:"warning_bytes"`
	CriticalBytes     uint64  `json:"critical_bytes"`
	HysteresisBytes   uint64  `json:"hysteresis_bytes"`
}

func DefaultPolicy() Policy { return Policy{15, 5, 2, 1 << 30, 256 << 20, 128 << 20} }
func (p Policy) Validate() error {
	if math.IsNaN(p.WarningPercent) || math.IsNaN(p.CriticalPercent) || math.IsNaN(p.HysteresisPercent) || p.CriticalPercent <= 0 || p.WarningPercent <= p.CriticalPercent || p.HysteresisPercent <= 0 || p.WarningPercent+p.HysteresisPercent >= 100 || p.CriticalBytes == 0 || p.WarningBytes <= p.CriticalBytes || p.HysteresisBytes == 0 || p.WarningBytes > math.MaxUint64-p.HysteresisBytes {
		return errors.New("invalid capacity thresholds")
	}
	return nil
}

// Prepare resolves not-yet-created directories to their nearest existing parent.
// Only missing paths permit fallback: permission and I/O errors are never hidden.
func Prepare(paths []Path) ([]Path, error) {
	out := make([]Path, 0, len(paths))
	for _, p := range paths {
		if !filepath.IsAbs(p.Name) || p.Label == "" {
			return nil, errors.New("capacity paths must be labelled and absolute")
		}
		name := filepath.Clean(p.Name)
		for {
			_, err := os.Stat(name)
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) || filepath.Dir(name) == name {
				return nil, fmt.Errorf("capacity %s: %w", p.Label, err)
			}
			name = filepath.Dir(name)
		}
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil {
			return nil, err
		}
		out = append(out, Path{p.Label, resolved})
	}
	return out, nil
}

// Collect samples each device once. Partial results are never returned as healthy.
func Collect(ctx context.Context, paths []Path, r Reader) ([]Volume, error) {
	volumes := map[uint64]*Volume{}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dev, err := r.Device(p.Name)
		if err != nil {
			return nil, fmt.Errorf("capacity %s: %w", p.Label, err)
		}
		v := volumes[dev]
		if v == nil {
			sample, err := r.Sample(p.Name)
			if err != nil {
				return nil, fmt.Errorf("capacity %s: %w", p.Label, err)
			}
			if sample.Total == 0 || sample.Available > sample.Total || sample.FreeFiles > sample.Files {
				return nil, fmt.Errorf("capacity %s: invalid filesystem counters", p.Label)
			}
			v = &Volume{Device: dev, Sample: sample}
			volumes[dev] = v
		}
		v.Labels = append(v.Labels, p.Label)
	}
	out := make([]Volume, 0, len(volumes))
	for _, v := range volumes {
		sort.Strings(v.Labels)
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	if len(out) == 0 {
		return nil, errors.New("no capacity paths")
	}
	return out, nil
}
func below(s Sample, percent float64, bytes uint64) bool {
	return s.Available < bytes || float64(s.Available)/float64(s.Total)*100 < percent || (s.Files > 0 && float64(s.FreeFiles)/float64(s.Files)*100 < percent)
}
func (p Policy) level(s Sample, previous string) string {
	if below(s, p.CriticalPercent, p.CriticalBytes) {
		return "error"
	}
	if previous == "error" && below(s, p.CriticalPercent+p.HysteresisPercent, p.CriticalBytes+p.HysteresisBytes) {
		return "error"
	}
	if below(s, p.WarningPercent, p.WarningBytes) {
		return "warning"
	}
	if previous != "" && below(s, p.WarningPercent+p.HysteresisPercent, p.WarningBytes+p.HysteresisBytes) {
		return "warning"
	}
	return ""
}

const Prefix = "capacity.fs."

func Key(device uint64) string { return fmt.Sprintf("%s%x", Prefix, device) }

// Apply uses the published incident as hysteresis state. Counts in an unchanged
// level do not rewrite the snapshot; concurrent publishers can safely retry it.
func Apply(s alertchannel.Snapshot, realm string, volumes []Volume, p Policy, now time.Time) (alertchannel.Snapshot, bool, error) {
	if err := p.Validate(); err != nil {
		return s, false, err
	}
	if len(volumes) == 0 {
		return s, false, errors.New("empty capacity observation")
	}
	changed := false
	seen := map[string]bool{}
	for _, v := range volumes {
		key := Key(v.Device)
		seen[key] = true
		var old alertchannel.Incident
		for _, i := range s.Incidents {
			if i.Key == key {
				old = i
				break
			}
		}
		previous := ""
		if old.Status == "active" {
			previous = old.Severity
		}
		level := p.level(v.Sample, previous)
		if level == previous {
			continue
		}
		status := "active"
		severity := level
		text := fmt.Sprintf("Server storage (%s): low capacity.", strings.Join(v.Labels, ", "))
		if level == "error" {
			text = fmt.Sprintf("Server storage (%s): critically low capacity; writes may fail.", strings.Join(v.Labels, ", "))
		}
		if level == "" {
			status = "resolved"
			severity = old.Severity
			text = fmt.Sprintf("Server storage (%s): capacity recovered.", strings.Join(v.Labels, ", "))
		}
		next, did, err := s.Change(realm, key, severity, status, text, now)
		if err != nil {
			return s, false, err
		}
		s = next
		changed = changed || did
	}
	// Device identities can change after a remount/configuration change. Do not
	// claim recovery of a filesystem no longer represented by the measurement.
	for _, i := range append([]alertchannel.Incident(nil), s.Incidents...) {
		if strings.HasPrefix(i.Key, Prefix) && i.Status == "active" && !seen[i.Key] {
			next, did, err := s.Change(realm, i.Key, i.Severity, "resolved", "Server storage: monitoring scope changed; this filesystem is no longer observed.", now)
			if err != nil {
				return s, false, err
			}
			s = next
			changed = changed || did
		}
	}
	return s, changed, nil
}
func product(a, b uint64) uint64 {
	if b != 0 && a > math.MaxUint64/b {
		return math.MaxUint64
	}
	return a * b
}
