package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/historyindex"
	"filees/pkg/shout"
)

type gatedIndexSource struct {
	mu    sync.Mutex
	head  int64
	gate  chan struct{}
	fail  error
	heads int
}

func (g *gatedIndexSource) Head(ctx context.Context) (int64, error) {
	g.mu.Lock()
	g.heads++
	gate := g.gate
	g.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return g.head, g.fail
}

func (g *gatedIndexSource) Log(_ context.Context, newest, oldest int64, _ int) ([]historyindex.Commit, error) {
	var out []historyindex.Commit
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	for rev := newest; rev >= oldest; rev-- {
		out = append(out, historyindex.Commit{Revision: rev, Date: base.Add(time.Duration(rev) * time.Minute).Format(time.RFC3339), Paths: []string{"doc.txt"}})
	}
	return out, nil
}

const densityUUID = "3f1d6a4e-0000-4000-8000-00000000beef"

func TestHistoryDensityIndexesInTheBackgroundOncePerRepository(t *testing.T) {
	src := &gatedIndexSource{head: 30, gate: make(chan struct{})}
	sources := 0
	clock := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	d := newHistoryDensityService(t.TempDir(), func(string, string) (historyindex.Source, error) {
		sources++
		return src, nil
	})
	d.index.Page = 10
	d.now = func() time.Time { return clock }
	q := historyindex.Query{BucketSeconds: 86400}

	first, err := d.HistoryDensity(t.Context(), "office", "svn://office/projects", densityUUID, q)
	if err != nil || !first.Indexing || first.Indexed != 0 {
		t.Fatalf("first answer = %+v %v", first, err)
	}
	if second, _ := d.HistoryDensity(t.Context(), "office", "svn://office/projects", densityUUID, q); !second.Indexing {
		t.Fatalf("second answer = %+v", second)
	}
	close(src.gate)
	d.group.Wait()
	if sources != 1 {
		t.Fatalf("runs started = %d, want one while a run is in progress", sources)
	}

	done, err := d.HistoryDensity(t.Context(), "office", "svn://office/projects", densityUUID, q)
	if err != nil || done.Indexing || done.Indexed != 30 || done.Head != 30 || len(done.Buckets) != 1 || done.Buckets[0].Commits != 30 {
		t.Fatalf("after the run = %+v %v", done, err)
	}
	d.group.Wait()
	if sources != 1 {
		t.Fatalf("a current index checked HEAD again within %s", historyIndexRecheck)
	}
	clock = clock.Add(historyIndexRecheck + time.Second)
	if _, err := d.HistoryDensity(t.Context(), "office", "svn://office/projects", densityUUID, q); err != nil {
		t.Fatal(err)
	}
	d.group.Wait()
	if sources != 2 {
		t.Fatalf("HEAD not rechecked after %s: runs = %d", historyIndexRecheck, sources)
	}
}

func TestHistoryDensityReportsAFailedRunAndWaitsBeforeRetrying(t *testing.T) {
	src := &gatedIndexSource{fail: errors.New("connection refused")}
	sources := 0
	d := newHistoryDensityService(t.TempDir(), func(string, string) (historyindex.Source, error) {
		sources++
		return src, nil
	})
	clock := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return clock }
	q := historyindex.Query{BucketSeconds: 3600}
	if _, err := d.HistoryDensity(t.Context(), "office", "svn://office/projects", densityUUID, q); err != nil {
		t.Fatal(err)
	}
	d.group.Wait()
	failed, err := d.HistoryDensity(t.Context(), "office", "svn://office/projects", densityUUID, q)
	if err != nil || failed.Indexing || failed.Diagnostic == "" || len(failed.Buckets) != 0 {
		t.Fatalf("after failure = %+v %v", failed, err)
	}
	d.group.Wait()
	if sources != 1 {
		t.Fatalf("failed run retried immediately: %d", sources)
	}
}

func TestHistoryIndexSourceMarksShoutsAndKeepsEveryPath(t *testing.T) {
	reader := &fakeHistoryReader{commits: []client.HistoryCommit{
		{Revision: 4, Date: "2026-09-14T11:00:00.000000Z", Message: shout.Format("nowe rzuty"), Changes: []client.HistoryChange{{Path: "A"}, {Path: "B/c.txt"}}},
		{Revision: 3, Date: "2026-09-14T10:00:00.000000Z", Message: "filees: automatic sync", Changes: []client.HistoryChange{{Path: ""}}},
	}, head: 4}
	src := historyIndexSource{reader: reader, url: "svn://office/projects"}
	commits, err := src.Log(t.Context(), 4, 3, 2)
	if err != nil || len(commits) != 2 || !commits[0].Shout || commits[1].Shout || len(commits[0].Paths) != 2 || commits[0].Paths[1] != "B/c.txt" {
		t.Fatalf("commits = %+v %v", commits, err)
	}
	if head, err := src.Head(t.Context()); err != nil || head != 4 {
		t.Fatalf("head = %d %v", head, err)
	}
}

type fakeHistoryReader struct {
	client.HistoryReader
	commits []client.HistoryCommit
	head    int64
}

func (f *fakeHistoryReader) HistoryLog(context.Context, string, int64, int64, int) ([]client.HistoryCommit, error) {
	return f.commits, nil
}

func (f *fakeHistoryReader) HistoryRevisionAt(context.Context, string, time.Time) (int64, string, error) {
	return f.head, "", nil
}
