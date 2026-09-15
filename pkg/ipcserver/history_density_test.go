package ipcserver

import (
	"context"
	"errors"
	"testing"
	"time"

	contract "filees/pkg/contract/v1"
	"filees/pkg/historyindex"
)

type densityStub struct {
	queries []historyindex.Query
	targets []string
	result  HistoryDensity
	err     error
}

func (d *densityStub) HistoryDensity(_ context.Context, serverID, repoURL, uuid string, q historyindex.Query) (HistoryDensity, error) {
	d.queries = append(d.queries, q)
	d.targets = append(d.targets, serverID+" "+repoURL+" "+uuid)
	return d.result, d.err
}

func densityServer(t *testing.T) (*Server, *historyStub, *densityStub, string) {
	t.Helper()
	stub := newHistoryStub(5)
	s := historyServer(stub)
	density := &densityStub{}
	s.SetHistoryDensityService(density)
	snap := historyCall[contract.RepoHistorySnapshot](t, s, contract.CmdRepoHistoryResolve, contract.RepoHistoryResolvePayload{ServerID: "office", RepoID: "repo-1"})
	return s, stub, density, snap.SnapshotID
}

func TestHistoryDensityCapabilityNeedsAServiceAndAHelper(t *testing.T) {
	advertised := func(s *Server) bool {
		for _, capability := range s.capabilities() {
			if capability == contract.CapRepoHistoryDensity {
				return true
			}
		}
		return false
	}
	stub := newHistoryStub(1)
	s := historyServer(stub)
	if advertised(s) {
		t.Fatal("advertised without a density service")
	}
	s.SetHistoryDensityService(&densityStub{})
	if !advertised(s) {
		t.Fatal("not advertised")
	}
	stub.enabled = false
	if advertised(s) {
		t.Fatal("advertised without a helper")
	}
}

func TestHistoryDensityAsksTheIndexForTheSnapshotRepository(t *testing.T) {
	s, _, density, snapshot := densityServer(t)
	start := historyEpoch.Unix()
	density.result = HistoryDensity{
		Result: historyindex.Result{
			FirstUnix: start + 60, LastUnix: start + 7200, Indexed: 5, More: true,
			Buckets: []historyindex.Bucket{{Start: start, End: start + 7200, ChangedPaths: 9, UniquePaths: 4, UniqueExact: true, Commits: 3, Shouts: 1}},
		},
		Head: 9, Indexing: true, Diagnostic: "",
	}
	from, to := historyEpoch.Format(time.RFC3339), historyEpoch.Add(48*time.Hour).Format(time.RFC3339)
	got := historyCall[contract.RepoHistoryDensityResult](t, s, contract.CmdRepoHistoryDensity, contract.RepoHistoryDensityPayload{
		SnapshotID: snapshot, BucketHours: 2, UTCOffsetMinutes: 120, From: from, To: to,
	})
	if got.HeadRevision != 9 || got.IndexedRevision != 5 || !got.Indexing || got.FirstDate != historyEpoch.Add(time.Minute).Format(time.RFC3339) ||
		len(got.Buckets) != 1 || got.Buckets[0].Start != historyEpoch.Format(time.RFC3339) || got.Buckets[0].ChangedPaths != 9 ||
		got.Buckets[0].UniquePaths != 4 || !got.Buckets[0].UniqueExact || got.Buckets[0].Shouts != 1 || got.NextCursor == "" {
		t.Fatalf("result = %+v", got)
	}
	q := density.queries[0]
	if q.BucketSeconds != 7200 || q.OffsetSeconds != 7200 || q.Limit != historyDensityPage || !q.From.Equal(historyEpoch) || !q.To.Equal(historyEpoch.Add(48*time.Hour)) || q.After != 0 {
		t.Fatalf("query = %+v", q)
	}
	if density.targets[0] != "office svn://office/projects uuid-1" {
		t.Fatalf("target = %s", density.targets[0])
	}

	historyCall[contract.RepoHistoryDensityResult](t, s, contract.CmdRepoHistoryDensity, contract.RepoHistoryDensityPayload{
		SnapshotID: snapshot, BucketHours: 24, Cursor: got.NextCursor,
	})
	if density.queries[1].After != start {
		t.Fatalf("cursor did not continue after the last bucket: %+v", density.queries[1])
	}
}

func TestHistoryDensityRefusesBadQueriesAndGuests(t *testing.T) {
	s, _, density, snapshot := densityServer(t)
	for name, payload := range map[string]contract.RepoHistoryDensityPayload{
		"three-hour bars": {SnapshotID: snapshot, BucketHours: 3},
		"no granularity":  {SnapshotID: snapshot},
		"zone too far":    {SnapshotID: snapshot, BucketHours: 1, UTCOffsetMinutes: 15 * 60},
		"reversed window": {SnapshotID: snapshot, BucketHours: 1, From: historyAt(10), To: historyAt(5)},
		"unparsed from":   {SnapshotID: snapshot, BucketHours: 1, From: "yesterday"},
		"foreign cursor":  {SnapshotID: snapshot, BucketHours: 1, Cursor: "r5:1"},
		"zero cursor":     {SnapshotID: snapshot, BucketHours: 1, Cursor: "b0"},
	} {
		if key := historyRefusal(t, s, contract.CmdRepoHistoryDensity, payload); key != "history.invalid_request" {
			t.Errorf("%s: %s", name, key)
		}
	}
	if key := historyRefusal(t, s, contract.CmdRepoHistoryDensity, contract.RepoHistoryDensityPayload{SnapshotID: "nope", BucketHours: 1}); key != "history.snapshot_unknown" {
		t.Fatalf("unknown snapshot: %s", key)
	}
	density.err = errors.New("index broke")
	if key := historyRefusal(t, s, contract.CmdRepoHistoryDensity, contract.RepoHistoryDensityPayload{SnapshotID: snapshot, BucketHours: 1}); key != "history.read_failed" {
		t.Fatalf("service failure: %s", key)
	}
	calls := len(density.queries)
	s.RegisterProjectedRepoPolicy("repo-1", "Projects", "svn://office/projects", "office", "rw", "active", "someone-else", "optional", true)
	if key := historyRefusal(t, s, contract.CmdRepoHistoryDensity, contract.RepoHistoryDensityPayload{SnapshotID: snapshot, BucketHours: 1}); key != "history.forbidden" {
		t.Fatalf("guest: %s", key)
	}
	if len(density.queries) != calls {
		t.Fatal("the index was asked for a refused request")
	}
}
