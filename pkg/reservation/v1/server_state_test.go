package v1

import (
	"encoding/json"
	"testing"
	"time"
)

func TestServerMetadataContractIsIndependentOfRepositories(t *testing.T) {
	if _, err := ParseRequest([]byte(`{"schema":"filees.reservation/v2","repo_id":""}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRequest([]byte(`{"schema":"filees.reservation/v1","repo_id":""}`)); err == nil {
		t.Fatal("v1 accepted server selector")
	}
	now := time.Now()
	good := Result{Schema: StateSchema, ServerID: "spot", ServerDisplayName: "40rs:filees", ViewGeneration: 26, ViewGeneratedAt: &now}
	cases := []struct {
		name   string
		mutate func(*Result)
	}{
		{"legacy", func(r *Result) { r.Schema = Schema }},
		{"no_name", func(r *Result) { r.ServerDisplayName = "" }},
		{"invalid_name", func(r *Result) { r.ServerDisplayName = "name\n" }},
		{"no_id", func(r *Result) { r.ServerID = "" }},
		{"no_view", func(r *Result) { r.ViewGeneration = 0 }},
		{"no_date", func(r *Result) { r.ViewGeneratedAt = nil }},
		{"repo_state", func(r *Result) { r.RepositoryState = "deleted" }},
		{"stale", func(r *Result) { r.Stale = true }},
		{"unknown", func(r *Result) { r.Unknown = true }},
		{"artifact", func(r *Result) { r.Generation = "1" }},
		{"locks", func(r *Result) { r.Reservations = []Reservation{{Path: "secret"}} }},
	}
	raw, _ := json.Marshal(good)
	if _, err := ParseResult(raw); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			tc.mutate(&r)
			raw, _ := json.Marshal(r)
			if _, err := ParseResult(raw); err == nil {
				t.Fatal("invalid server state accepted")
			}
		})
	}
}
