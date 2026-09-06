package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"filees/pkg/pathownership"
)

func TestAutolockWireValidation(t *testing.T) {
	base := func() Result {
		return Result{Schema: AutolockSchema, RepoID: repoID, RepositoryState: "active", Generation: "1", AsOf: time.Now(), PathOwnership: &pathownership.Snapshot{RepositoryUUID: repoID, Revision: 1, Entries: []pathownership.Entry{{Path: "Łódź/file", Object: pathownership.Object{ID: strings.Repeat("a", 64), CreatedRevision: 1, FirstCommitter: repoID, Kind: "file"}, OwnerRealmID: repoID}}}}
	}
	for _, tc := range []struct {
		name   string
		change func(*Result)
		valid  bool
	}{
		{"fresh", func(*Result) {}, true},
		{"unknown ownership", func(r *Result) { r.PathOwnership = nil; r.OwnershipDetail = "legacy grant" }, true},
		{"absent ownership", func(r *Result) { r.PathOwnership = nil }, false},
		{"legacy schema", func(r *Result) { r.Schema = StateSchema }, false},
		{"stale", func(r *Result) { r.Stale = true }, false},
		{"traversal", func(r *Result) { r.PathOwnership.Entries[0].Path = "../x" }, false},
		{"duplicate", func(r *Result) { r.PathOwnership.Entries = append(r.PathOwnership.Entries, r.PathOwnership.Entries[0]) }, false},
		{"unknown owner", func(r *Result) { r.PathOwnership.Entries[0].OwnerRealmID = "" }, false},
		{"future birth", func(r *Result) { r.PathOwnership.Entries[0].CreatedRevision = 2 }, false},
		{"incarnation", func(r *Result) { r.PathOwnership.RepositoryUUID = "" }, false},
		{"duplicate holds", func(r *Result) { r.Reservations = []Reservation{{Path: "a", Token: "one"}, {Path: "a", Token: "two"}} }, false},
		{"hold traversal", func(r *Result) { r.Reservations = []Reservation{{Path: "../a", Token: "one"}} }, false},
		{"empty hold token", func(r *Result) { r.Reservations = []Reservation{{Path: "a"}} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.change(&r)
			raw, _ := json.Marshal(r)
			_, err := ParseResult(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	if err := (Request{Schema: AutolockSchema, RepoID: repoID}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Request{Schema: AutolockSchema}).Validate(); err == nil {
		t.Fatal("empty selector")
	}
}
