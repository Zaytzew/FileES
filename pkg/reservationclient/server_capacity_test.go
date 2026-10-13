package reservationclient

import (
	v1 "filees/pkg/reservation/v1"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerCapacityCacheTransitExpiryAndUnknownRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server-state.json")
	now := time.Now().UTC()
	r := v1.Result{Schema: v1.StateSchema, ServerID: "spot", ServerDisplayName: "Spot", ViewGeneration: 1, ViewGeneratedAt: &now, Reservations: []v1.Reservation{}, StorageCapacity: &v1.StorageCapacity{State: "observed", TotalBytes: 1000, AvailableBytes: 200, MeasuredAt: &now, ValidForSeconds: 180}}
	if err := StoreServerState(path, "spot", r, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	stored, _, err := LoadServerState(path, "spot")
	if err != nil || stored.StorageCapacity.ValidForSeconds != 178 {
		t.Fatal(stored, err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at   time.Time
		want string
	}{{now.Add(time.Second), "fresh"}, {now.Add(178 * time.Second), "stale"}, {now.Add(-time.Second), "stale"}} {
		value := LoadServerCapacity(path, "spot", tc.at)
		if value.State != tc.want || value.AvailableBytes != 200 {
			t.Fatal(value, tc)
		}
	}
	before, _ := os.Stat(path)
	_ = LoadServerCapacity(path, "spot", now)
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("cache read refreshed sample")
	}
	oldReceipt := now.Add(-200 * time.Second)
	if err := os.Chtimes(path, oldReceipt, oldReceipt); err != nil {
		t.Fatal(err)
	}
	r.ViewGeneration = 2
	if err := StoreServerState(path, "spot", r); err != nil {
		t.Fatal(err)
	}
	if got := LoadServerCapacity(path, "spot", time.Now()); got.State != "stale" {
		t.Fatal("metadata update renewed old capacity", got)
	}
	r.StorageCapacity = nil // old server or unavailable measurement
	if err := StoreServerState(path, "spot", r); err != nil {
		t.Fatal(err)
	}
	value := LoadServerCapacity(path, "spot", time.Now())
	if value.State != "stale" || value.AvailableBytes != 200 {
		t.Fatal(value)
	}
	if value := LoadServerCapacity(path, "foreign", now); value.State != "unknown" {
		t.Fatal("cross-server cache", value)
	}
	r.StorageCapacity = &v1.StorageCapacity{State: "observed", TotalBytes: 1000, AvailableBytes: 0, MeasuredAt: &now, ValidForSeconds: 180}
	if err := StoreServerState(path, "spot", r, 181*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := LoadServerCapacity(path, "spot", time.Now()); got.State != "stale" || got.AvailableBytes != 0 {
		t.Fatal(got)
	}
}

func TestServerCapacityMissingOrInvalidCacheIsNeverZeroFreeSpace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for _, raw := range [][]byte{nil, []byte(`{"broken":true}`), make([]byte, 65537)} {
		if raw != nil {
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if got := LoadServerCapacity(path, "spot", time.Now()); got.State != "unknown" || got.TotalBytes != 0 || got.MeasuredAt != "" {
			t.Fatal(got)
		}
	}
}
