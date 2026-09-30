package contracttests

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "filees/pkg/reservation/v1"
)

func TestStorageWriteWireScopeAndLifetime(t *testing.T) {
	const repo = "11111111-1111-4111-8111-111111111111"
	now := time.Now().UTC()
	base := v1.Result{Schema: v1.StateSchema, RepoID: repo, RepositoryState: "active", Unknown: true, Reservations: []v1.Reservation{}}
	for _, signal := range []*v1.StorageWrite{nil, {State: "unknown"}, {State: "available", MeasuredAt: &now, ValidForSeconds: 180}, {State: "blocked", MeasuredAt: &now, ValidForSeconds: 1}} {
		base.StorageWrite = signal
		raw, _ := json.Marshal(base)
		if _, err := v1.ParseResult(raw); err != nil {
			t.Fatal(string(raw), err)
		}
		if signal == nil && strings.Contains(string(raw), "storage_write") {
			t.Fatal("legacy response changed")
		}
	}
	for _, signal := range []*v1.StorageWrite{{State: ""}, {State: "unknown", MeasuredAt: &now}, {State: "available", ValidForSeconds: 180}, {State: "blocked", MeasuredAt: &now, ValidForSeconds: 181}, {State: "available", MeasuredAt: &now, ValidForSeconds: 0}} {
		base.StorageWrite = signal
		raw, _ := json.Marshal(base)
		if _, err := v1.ParseResult(raw); err == nil {
			t.Fatal("accepted invalid signal", string(raw))
		}
	}
	for _, schema := range []string{v1.Schema, v1.StateSchema, v1.AutolockSchema} {
		r := v1.Request{Schema: schema, RepoID: repo, IncludeStorageWrite: true}
		if (r.Validate() == nil) != (schema != v1.Schema) {
			t.Fatal(r)
		}
		r.RepoID = ""
		if r.Validate() == nil {
			t.Fatal("server selector exposed storage", r)
		}
	}
	base.StorageWrite = &v1.StorageWrite{State: "unknown"}
	base.RepositoryState = "deleted"
	raw, _ := json.Marshal(base)
	if _, err := v1.ParseResult(raw); err == nil {
		t.Fatal("deleted repo carried write signal")
	}
}
