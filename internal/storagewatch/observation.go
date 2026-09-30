package storagewatch

import (
	"errors"
	"time"

	"filees/pkg/alertchannel"
)

// ObservationTTL covers the one-minute cron and its bounded execution time.
// This is a snapshot lifetime, never a reservation or a write authorization.
const ObservationTTL = 3 * time.Minute

type DeviceWriteState struct {
	Device uint64 `json:"device"`
	State  string `json:"state"`
}

// Observation is private server state. Device identities never go to clients.
type Observation struct {
	MeasuredAt time.Time          `json:"measured_at"`
	Devices    []DeviceWriteState `json:"devices"`
}

func newObservation(volumes []Volume, snapshot alertchannel.Snapshot, now time.Time) (*Observation, error) {
	o := &Observation{MeasuredAt: now.UTC()}
	for _, v := range volumes {
		if v.Sample.Total == 0 || v.Sample.Available > v.Sample.Total || v.Sample.FreeFiles > v.Sample.Files {
			return nil, errors.New("invalid capacity observation counters")
		}
		state := "available"
		for _, incident := range snapshot.Incidents {
			if incident.Key == Key(v.Device) && incident.Status == "active" && incident.Severity == "error" {
				state = "blocked"
			}
		}
		o.Devices = append(o.Devices, DeviceWriteState{v.Device, state})
	}
	return o, o.validate()
}

func (o *Observation) validate() error {
	if o == nil {
		return nil
	} // old state has no observation, not "available"
	if o.MeasuredAt.IsZero() || len(o.Devices) == 0 || len(o.Devices) > 128 {
		return errors.New("invalid capacity observation")
	}
	seen := map[uint64]bool{}
	for _, d := range o.Devices {
		if seen[d.Device] || (d.State != "available" && d.State != "blocked") {
			return errors.New("invalid capacity observation device")
		}
		seen[d.Device] = true
	}
	return nil
}

// WriteState requires coverage of EVERY volume used by the operation. A
// missing/remounted volume, expired sample or backward clock means unknown.
// The remaining lifetime is for a client's local elapsed-time clock.
func (o *Observation) WriteState(devices []uint64, now time.Time) (string, time.Time, time.Duration) {
	if o == nil || o.validate() != nil || len(devices) == 0 || now.Before(o.MeasuredAt) || !now.Before(o.MeasuredAt.Add(ObservationTTL)) {
		return "unknown", time.Time{}, 0
	}
	state := "available"
	for _, device := range devices {
		found := false
		for _, d := range o.Devices {
			if d.Device == device {
				found = true
				if d.State == "blocked" {
					state = "blocked"
				}
				break
			}
		}
		if !found {
			return "unknown", time.Time{}, 0
		}
	}
	return state, o.MeasuredAt, o.MeasuredAt.Add(ObservationTTL).Sub(now)
}
