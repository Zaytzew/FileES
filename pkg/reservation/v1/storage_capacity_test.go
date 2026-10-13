package v1

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStorageCapacityIsOptInServerMetadataOnly(t *testing.T) {
	for _, req := range []Request{{Schema: Schema, RepoID: "a", IncludeStorageCapacity: true}, {Schema: AutolockSchema, RepoID: "a", IncludeStorageCapacity: true}, {Schema: StateSchema, RepoID: "a", IncludeStorageCapacity: true}} {
		if req.Validate() == nil {
			t.Fatal("wrong selector accepted", req)
		}
	}
	if err := (Request{Schema: StateSchema, IncludeStorageCapacity: true}).Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := Result{Schema: StateSchema, ServerID: "spot", ServerDisplayName: "Spot", ViewGeneration: 1, ViewGeneratedAt: &now, Reservations: []Reservation{}}
	for _, sample := range []StorageCapacity{{State: "observed", TotalBytes: 1000, AvailableBytes: 0, MeasuredAt: &now, ValidForSeconds: 180}, {State: "unknown"}, {State: "observed", TotalBytes: 1000, AvailableBytes: 1000, MeasuredAt: &now}} {
		base.StorageCapacity = &sample
		raw, _ := json.Marshal(base)
		if _, err := ParseResult(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, sample := range []StorageCapacity{{State: "unknown", TotalBytes: 100}, {State: "observed", TotalBytes: 100, AvailableBytes: 101, MeasuredAt: &now}, {State: "observed", TotalBytes: 100, MeasuredAt: &now, ValidForSeconds: 181}, {State: "observed", TotalBytes: 100}, {State: "observed", TotalBytes: 1 << 54, MeasuredAt: &now}} {
		base.StorageCapacity = &sample
		raw, _ := json.Marshal(base)
		if _, err := ParseResult(raw); err == nil {
			t.Fatal("bad sample accepted", sample)
		}
	}
	base.StorageCapacity = &StorageCapacity{State: "unknown"}
	base.RepoID = "123"
	raw, _ := json.Marshal(base)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("capacity leaked into repository selector")
	}
}
