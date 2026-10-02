package storagewatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWriteHoldPersistsUntilFreshCoveredHystereticRecovery(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "capacity.json")
	s, err := Load(path, uuid.NewString(), "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := Policy{15, 5, 2, 100, 25, 10}
	volumes := func(device, free uint64) []Volume {
		return []Volume{{Device: device, Sample: Sample{Total: 1000, Available: free}}}
	}
	if err := s.Observe(volumes(1, 40), p, now); err != nil {
		t.Fatal(err)
	}
	reload := func() {
		t.Helper()
		if err := s.Save(path); err != nil {
			t.Fatal(err)
		}
		s, err = Load(path, s.Realm, s.Recipient)
		if err != nil {
			t.Fatal(err)
		}
	}
	reload()
	if state, _, _ := s.Observation.WriteState([]uint64{1}, now.Add(time.Hour)); state != "unknown" || !s.WritesHeld() {
		t.Fatal("expired observation released durable hold", state, s)
	}
	// A complete new measurement of a DIFFERENT device cannot prove recovery.
	if err := s.Observe(volumes(2, 800), p, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	reload()
	if !s.WritesHeld() || len(s.WriteHolds) != 1 || s.WriteHolds[0].Device != 1 || !s.WriteHolds[0].Since.Equal(now) {
		t.Fatal("changed scope released old device", s)
	}
	// A returning device below the recovery boundary stays held, even though
	// its old incident was resolved when it disappeared from monitoring.
	if err := s.Observe(volumes(1, 60), p, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	reload()
	if state, _, _ := s.Observation.WriteState([]uint64{1}, now.Add(2*time.Hour)); state != "blocked" || !s.WritesHeld() {
		t.Fatal("lost hysteresis on returning device", state, s)
	}
	// Old or equal timestamps never release a hold or advertise it available.
	beforeClock, _ := json.Marshal(s)
	if err := s.Observe(volumes(1, 800), p, now.Add(90*time.Minute)); err == nil {
		t.Fatal("backwards clock accepted")
	}
	afterClock, _ := json.Marshal(s)
	if string(beforeClock) != string(afterClock) {
		t.Fatal("backwards clock changed state")
	}
	if err := s.Observe(volumes(1, 800), p, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	reload()
	if state, _, _ := s.Observation.WriteState([]uint64{1}, now.Add(2*time.Hour)); state != "blocked" || !s.WritesHeld() {
		t.Fatal("equal timestamp released hold", state, s)
	}
	if err := s.Observe(volumes(1, 70), p, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	reload()
	if s.WritesHeld() || len(s.WriteHolds) != 0 {
		t.Fatal("fresh recovery did not release hold", s)
	}
}

func TestWriteHoldMigratesExpiredLegacyObservationWithoutRewritingFile(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "capacity.json")
	legacy := State{Schema: stateSchema, Realm: uuid.NewString(), Recipient: "admin@example.test", Observation: &Observation{MeasuredAt: now.Add(-time.Hour), Devices: []DeviceWriteState{{Device: 1, State: "blocked"}}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path, legacy.Realm, legacy.Recipient)
	if err != nil || !s.WritesHeld() || len(s.WriteHolds) != 1 {
		t.Fatal("legacy critical sample was forgotten", s, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("read rewrote legacy state", err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, legacy.Realm, legacy.Recipient)
	if err != nil || !reflect.DeepEqual(loaded.WriteHolds, s.WriteHolds) {
		t.Fatal("migration not durable", loaded, err)
	}
}

func TestWriteHoldInvalidInputPreservesHeldState(t *testing.T) {
	now := time.Now().UTC()
	s := State{Realm: uuid.NewString(), WriteHolds: []WriteHold{{Device: 1, Since: now}}}
	before := s
	if err := s.Observe([]Volume{{Device: 1, Sample: Sample{Total: 100, Available: 101}}}, DefaultPolicy(), now.Add(time.Minute)); err == nil || !reflect.DeepEqual(s, before) {
		t.Fatal("invalid observation changed hold", s, err)
	}
	for _, holds := range [][]WriteHold{
		{{Device: 1}},
		{{Device: 1, Since: now}, {Device: 1, Since: now}},
		make([]WriteHold, 129),
	} {
		if err := validateWriteHolds(holds); err == nil {
			t.Fatal("malformed holds accepted", holds)
		}
	}
}
