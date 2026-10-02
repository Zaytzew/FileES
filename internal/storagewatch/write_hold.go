package storagewatch

import (
	"errors"
	"sort"
	"time"
)

// WriteHold is private, durable server state, not a client's observation TTL.
// Missing measurements (including a changed mount) never release a hold.
type WriteHold struct {
	Device uint64    `json:"device"`
	Since  time.Time `json:"since"`
}

func validateWriteHolds(holds []WriteHold) error {
	if len(holds) > 128 {
		return errors.New("too many capacity write holds")
	}
	seen := make(map[uint64]bool, len(holds))
	for _, hold := range holds {
		if hold.Since.IsZero() || seen[hold.Device] {
			return errors.New("invalid capacity write hold")
		}
		seen[hold.Device] = true
	}
	return nil
}

// migrateObservationHolds preserves a critical sample written by the old
// monitor, even after that sample expires. It does not turn absent old data
// into a positive authorization or modify the file during a read.
func migrateObservationHolds(holds []WriteHold, observation *Observation) []WriteHold {
	out := append([]WriteHold(nil), holds...)
	if observation == nil {
		return out
	}
	seen := make(map[uint64]bool, len(out))
	for _, hold := range out {
		seen[hold.Device] = true
	}
	for _, device := range observation.Devices {
		if device.State == "blocked" && !seen[device.Device] {
			out = append(out, WriteHold{Device: device.Device, Since: observation.MeasuredAt})
			seen[device.Device] = true
		}
	}
	return out
}

func nextWriteHolds(holds []WriteHold, previous *Observation, volumes []Volume, policy Policy, now time.Time) ([]WriteHold, error) {
	holds = migrateObservationHolds(holds, previous)
	if err := validateWriteHolds(holds); err != nil {
		return nil, err
	}
	byDevice := make(map[uint64]WriteHold, len(holds))
	for _, hold := range holds {
		byDevice[hold.Device] = hold
	}
	canRelease := previous == nil || now.After(previous.MeasuredAt)
	for _, volume := range volumes {
		hold, held := byDevice[volume.Device]
		level := ""
		if held {
			// Retain hysteresis even if an absent device was removed from the
			// alert snapshot while the hold itself remained durable.
			level = "error"
		}
		if policy.level(volume.Sample, level) == "error" {
			if !held {
				byDevice[volume.Device] = WriteHold{Device: volume.Device, Since: now.UTC()}
			}
		} else if held && canRelease && now.After(hold.Since) {
			delete(byDevice, volume.Device)
		}
	}
	out := make([]WriteHold, 0, len(byDevice))
	for _, hold := range byDevice {
		out = append(out, hold)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out, validateWriteHolds(out)
}

// WritesHeld reports the durable stop independently of snapshot expiration.
// The caller must handle failure to load/validate state separately; false is
// not a capacity reservation. This primitive does not itself stop any writer.
func (s State) WritesHeld() bool {
	if len(s.WriteHolds) != 0 {
		return true
	}
	if s.Observation != nil {
		for _, device := range s.Observation.Devices {
			if device.State == "blocked" {
				return true
			}
		}
	}
	return false
}
