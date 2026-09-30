package storagewatch

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestObservationFreshnessCoverageAndHysteresis(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "capacity.json")
	s, err := Load(path, uuid.NewString(), "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := Policy{15, 5, 2, 100, 25, 10}
	for i, tc := range []struct {
		free uint64
		want string
	}{{800, "available"}, {40, "blocked"}, {60, "blocked"}, {70, "available"}} {
		at := now.Add(time.Duration(i) * time.Minute)
		volumes := []Volume{{Device: 1, Sample: Sample{Total: 1000, Available: tc.free}}, {Device: 2, Sample: Sample{Total: 1000, Available: 800}}}
		if err := s.Observe(volumes, p, at); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(path); err != nil {
			t.Fatal(err)
		}
		s, err = Load(path, s.Realm, s.Recipient)
		if err != nil {
			t.Fatal(err)
		}
		state, measured, ttl := s.Observation.WriteState([]uint64{1, 2}, at.Add(time.Second))
		if state != tc.want || !measured.Equal(at) || ttl != ObservationTTL-time.Second {
			t.Fatalf("%s %s %v", state, measured, ttl)
		}
		for _, check := range []struct {
			devices []uint64
			at      time.Time
		}{
			{[]uint64{1, 3}, at}, {nil, at}, {[]uint64{1}, at.Add(-time.Second)}, {[]uint64{1}, at.Add(ObservationTTL)},
		} {
			state, stamp, ttl := s.Observation.WriteState(check.devices, check.at)
			if state != "unknown" || !stamp.IsZero() || ttl != 0 {
				t.Fatal("unknown became permission", state, stamp, ttl)
			}
		}
	}
	// A new healthy sample updates freshness without creating incidents or mail.
	before := len(s.Pending)
	if err := s.Observe([]Volume{{Device: 2, Sample: Sample{Total: 1000, Available: 800}}}, p, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(s.Pending) != before {
		t.Fatal("healthy heartbeat generated mail")
	}
	if state, _, _ := s.Observation.WriteState([]uint64{1}, now.Add(4*time.Minute)); state != "unknown" {
		t.Fatal("removed volume considered healthy")
	}
}

func TestObservationMissingAndMalformedAreUnknown(t *testing.T) {
	now := time.Now()
	for _, o := range []*Observation{nil, {}, {MeasuredAt: now, Devices: []DeviceWriteState{{1, ""}}}, {MeasuredAt: now, Devices: []DeviceWriteState{{1, "available"}, {1, "blocked"}}}} {
		if state, _, _ := o.WriteState([]uint64{1}, now); state != "unknown" {
			t.Fatal(state)
		}
	}
}
