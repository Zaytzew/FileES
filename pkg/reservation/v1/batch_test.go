package v1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/pathownership"
)

func batchFixture(n int) (clientview.View, map[string]Result) {
	v := clientview.View{ClientID: repoID, RealmID: repoID, Generation: 1}
	results := map[string]Result{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		v.Repositories = append(v.Repositories, clientview.Repository{RepoID: id, State: "active"})
		results[id] = Result{Schema: AutolockSchema, RepoID: id, RepositoryState: "active", AsOf: time.Now(), Generation: "1", ViewGeneration: 1, PathOwnership: &pathownership.Snapshot{RepositoryUUID: id, Revision: 1}}
	}
	return v, results
}

func fixtureBatch(v clientview.View, req BatchRequest, results map[string]Result) BatchResponse {
	ids := BatchInventory(v)
	page, next, _ := BatchPage(ids, req.After)
	r := BatchResponse{Schema: BatchSchema, ServerID: "lab", ClientID: v.ClientID, RealmID: v.RealmID, Scope: req.Scope, After: req.After, Next: next, Repositories: ids, Entries: []BatchEntry{}}
	for _, id := range page {
		r.Entries = append(r.Entries, NewBatchEntry(req.Scope, results[id], req.Known[id]))
	}
	return r
}

func TestBatchInitDeltaAndLockChangeWithoutCommit(t *testing.T) {
	v, source := batchFixture(55)
	cached := map[string]Result{}
	var initBytes, deltaBytes int
	for pass := 0; pass < 2; pass++ {
		req := BatchRequest{Schema: BatchSchema, Scope: BatchScope("lab", v), Known: map[string]string{}}
		for id, r := range cached {
			req.Known[id] = BatchVersion(req.Scope, r)
		}
		pages := 0
		for {
			response := fixtureBatch(v, req, source)
			bytes, _ := json.Marshal(response)
			if pass == 0 {
				initBytes += len(bytes)
			} else {
				deltaBytes += len(bytes)
			}
			if _, err := ParseBatchResponse(bytes); err != nil {
				t.Fatal(err)
			}
			if pass == 1 {
				for _, entry := range response.Entries {
					if entry.Result != nil {
						t.Fatal("unchanged payload sent")
					}
				}
			}
			applied, err := ApplyBatch(req, response, "lab", v.ClientID, v.RealmID, cached)
			if err != nil {
				t.Fatal(err)
			}
			for id, r := range applied {
				cached[id] = r
			}
			pages++
			if response.Next == "" {
				break
			}
			req.After = response.Next
		}
		if pages != 14 || len(cached) != 55 {
			t.Fatal(pages, len(cached))
		}
	}
	t.Logf("55 repos: init=%d bytes unchanged=%d bytes, 14 sessions instead of 55", initBytes, deltaBytes)
	req := BatchRequest{Schema: BatchSchema, Scope: BatchScope("lab", v), Known: map[string]string{}}
	for id, r := range cached {
		req.Known[id] = BatchVersion(req.Scope, r)
	}
	id := BatchInventory(v)[0]
	r := source[id]
	r.Reservations = []Reservation{{Path: "a", Token: "new-lock", OwnerRealmID: repoID}}
	source[id] = r
	response := fixtureBatch(v, req, source)
	if response.Entries[0].Result == nil {
		t.Fatal("lock changed without revision was omitted")
	}
	merged, err := ApplyBatch(req, response, "lab", v.ClientID, v.RealmID, cached)
	if err != nil || merged[id].Reservations[0].Token != "new-lock" {
		t.Fatal(err)
	}
	// Fresh observation fields change without changing the content hash.
	r = source[id]
	version := BatchVersion(req.Scope, r)
	r.AsOf = time.Now().Add(time.Hour)
	r.Generation = "900"
	if BatchVersion(req.Scope, r) != version {
		t.Fatal("observation counter changed content version")
	}
}

func TestBatchRejectsPartialMixedOrUnsupportedEvidence(t *testing.T) {
	v, source := batchFixture(5)
	req := BatchRequest{Schema: BatchSchema, Scope: BatchScope("lab", v), Known: map[string]string{}}
	for id, r := range source {
		req.Known[id] = BatchVersion(req.Scope, r)
	}
	for _, tc := range []struct {
		name   string
		change func(*BatchResponse)
	}{
		{"missing", func(r *BatchResponse) { r.Entries = r.Entries[:1] }},
		{"duplicate", func(r *BatchResponse) { r.Entries[1] = r.Entries[0] }},
		{"cursor", func(r *BatchResponse) { r.Next = "" }},
		{"scope", func(r *BatchResponse) { r.Scope = strings.Repeat("0", 64) }},
		{"actor", func(r *BatchResponse) { r.ClientID = "another" }},
		{"server", func(r *BatchResponse) { r.ServerID = "other" }},
		{"version", func(r *BatchResponse) { r.Entries[0].Version = strings.Repeat("0", 64) }},
		{"freshness", func(r *BatchResponse) { r.Entries[0].AsOf = time.Time{} }},
		{"storage", func(r *BatchResponse) {
			r.Entries[0].StorageWrite = &StorageWrite{State: "available", ValidForSeconds: 999}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fixtureBatch(v, req, source)
			tc.change(&r)
			applied, err := ApplyBatch(req, r, "lab", v.ClientID, v.RealmID, source)
			if err == nil || applied != nil {
				t.Fatal("partial page accepted")
			}
		})
	}
	if _, err := ApplyBatch(req, fixtureBatch(v, req, source), "lab", v.ClientID, v.RealmID, nil); err == nil {
		t.Fatal("missing baseline accepted")
	}
	empty, _ := batchFixture(0)
	er := BatchRequest{Schema: BatchSchema, Scope: BatchScope("lab", empty)}
	if r, err := ApplyBatch(er, fixtureBatch(empty, er, nil), "lab", empty.ClientID, empty.RealmID, nil); err != nil || len(r) != 0 {
		t.Fatal(err)
	}
	if _, _, err := BatchPage([]string{repoID, repoID}, ""); err == nil {
		t.Fatal("duplicate inventory")
	}
	if _, _, err := BatchPage([]string{repoID}, repoID); err == nil {
		t.Fatal("terminal cursor loop")
	}
	if _, _, err := BatchPage(make([]string, BatchMaxRepositories+1), ""); err == nil {
		t.Fatal("unbounded inventory")
	}
	raw, _ := json.Marshal(req)
	if _, err := ParseBatchRequest(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("trailing request")
	}
	if _, err := ParseBatchResponse([]byte(`{"schema":"broken"}`)); err == nil {
		t.Fatal("malformed response")
	}
}

func TestBatchScopeKeepsAuthorityNotHeartbeat(t *testing.T) {
	v, _ := batchFixture(1)
	first := BatchScope("lab", v)
	v.GeneratedAt = time.Now()
	if BatchScope("lab", v) != first {
		t.Fatal("heartbeat invalidated")
	}
	v.Generation++
	if BatchScope("lab", v) == first {
		t.Fatal("generation ignored")
	}
	v.Generation--
	v.Repositories[0].Access = "ro"
	if BatchScope("lab", v) == first {
		t.Fatal("rights ignored")
	}
}

func TestBatchLargeOwnershipPayloadSentOnlyOnce(t *testing.T) {
	v, source := batchFixture(1)
	id := v.Repositories[0].RepoID
	r := source[id]
	for i := 0; i < 1000; i++ {
		r.PathOwnership.Entries = append(r.PathOwnership.Entries, pathownership.Entry{Path: fmt.Sprintf("docs/%04d.txt", i), Object: pathownership.Object{ID: fmt.Sprintf("%064x", i), Kind: "file", CreatedRevision: 1}, OwnerRealmID: repoID})
	}
	source[id] = r
	req := BatchRequest{Schema: BatchSchema, Scope: BatchScope("lab", v)}
	init := fixtureBatch(v, req, source)
	full, _ := json.Marshal(init)
	cache, err := ApplyBatch(req, init, "lab", v.ClientID, v.RealmID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Known = map[string]string{id: BatchVersion(req.Scope, cache[id])}
	delta := fixtureBatch(v, req, source)
	small, _ := json.Marshal(delta)
	if _, err := ApplyBatch(req, delta, "lab", v.ClientID, v.RealmID, cache); err != nil {
		t.Fatal(err)
	}
	if len(small)*20 >= len(full) {
		t.Fatal("unchanged payload not reduced", len(full), len(small))
	}
	t.Logf("1000 ownership entries: init=%d B unchanged=%d B", len(full), len(small))
}
