package v1

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTerminalStateContract(t *testing.T) {
	now := time.Now().UTC()
	good := Result{Schema: StateSchema, RepoID: "11111111-1111-4111-8111-111111111111", RepositoryState: "deleted", Reservations: []Reservation{}, ViewGeneration: 7, ViewGeneratedAt: &now}
	cases := []struct {
		name string
		edit func(*Result)
		ok   bool
	}{
		{"deleted", func(*Result) {}, true},
		{"legacy", func(r *Result) { r.Schema = Schema }, false},
		{"stale", func(r *Result) { r.Stale = true }, false},
		{"unknown", func(r *Result) { r.Unknown = true }, false},
		{"locks", func(r *Result) { r.Reservations = []Reservation{{Path: "x"}} }, false},
		{"no_view", func(r *Result) { r.ViewGeneration = 0 }, false},
		{"no_time", func(r *Result) { r.ViewGeneratedAt = nil }, false},
		{"wrong_state", func(r *Result) { r.RepositoryState = "missing" }, false},
		{"artifact", func(r *Result) { r.Generation = "2" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := good
			c.edit(&r)
			raw, _ := json.Marshal(r)
			_, err := ParseResult(raw)
			if (err == nil) != c.ok {
				t.Fatalf("error=%v", err)
			}
		})
	}
	raw, _ := json.Marshal(good)
	if _, err := ParseResult(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
