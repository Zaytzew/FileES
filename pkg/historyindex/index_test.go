package historyindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// fakeSource is a repository whose revision n was committed at epoch + minutes[n].
type fakeSource struct {
	head    int64
	minutes map[int64]int
	paths   map[int64][]string
	shouts  map[int64]bool
	calls   []string
	broken  func(newest, oldest int64, commits []Commit) []Commit
}

func (f *fakeSource) Head(context.Context) (int64, error) { return f.head, nil }

func (f *fakeSource) Log(_ context.Context, newest, oldest int64, limit int) ([]Commit, error) {
	f.calls = append(f.calls, fmt.Sprintf("%d:%d/%d", newest, oldest, limit))
	var out []Commit
	for rev := newest; rev >= oldest; rev-- {
		out = append(out, Commit{
			Revision: rev,
			Date:     epoch.Add(time.Duration(f.minutes[rev]) * time.Minute).Format("2006-01-02T15:04:05.000000Z"),
			Shout:    f.shouts[rev],
			Paths:    f.paths[rev],
		})
	}
	if f.broken != nil {
		out = f.broken(newest, oldest, out)
	}
	return out, nil
}

func linearSource(head int64) *fakeSource {
	f := &fakeSource{head: head, minutes: map[int64]int{}, paths: map[int64][]string{}, shouts: map[int64]bool{}}
	for rev := int64(1); rev <= head; rev++ {
		f.minutes[rev] = int(rev) * 10
		f.paths[rev] = []string{fmt.Sprintf("/doc/%d.txt", rev)}
	}
	return f
}

const uuid = "3f1d6a4e-0000-4000-8000-00000000beef"

func TestExtendIndexesInPagesAndContinuesWhereItStopped(t *testing.T) {
	x := &Index{Dir: t.TempDir(), Page: 3}
	src := linearSource(7)
	indexed, head, err := x.Extend(t.Context(), uuid, src, 100)
	if err != nil || indexed != 7 || head != 7 {
		t.Fatalf("indexed=%d head=%d err=%v", indexed, head, err)
	}
	if strings.Join(src.calls, ",") != "3:1/3,6:4/3,7:7/1" {
		t.Fatalf("log calls = %v", src.calls)
	}
	src.head, src.minutes[8], src.minutes[9] = 9, 80, 90
	src.paths[8], src.paths[9] = []string{"/a"}, []string{"/b"}
	src.calls = nil
	if indexed, _, err := x.Extend(t.Context(), uuid, src, 100); err != nil || indexed != 9 || strings.Join(src.calls, ",") != "9:8/2" {
		t.Fatalf("second extend: indexed=%d calls=%v err=%v", indexed, src.calls, err)
	}
	if got, err := x.Indexed(uuid); err != nil || got != 9 {
		t.Fatalf("Indexed = %d %v", got, err)
	}
	src.calls = nil
	if indexed, _, err := x.Extend(t.Context(), uuid, src, 100); err != nil || indexed != 9 || len(src.calls) != 0 {
		t.Fatalf("an up-to-date index asked the log again: %v %v", src.calls, err)
	}
}

func TestExtendRespectsItsPageBudget(t *testing.T) {
	x := &Index{Dir: t.TempDir(), Page: 3}
	src := linearSource(10)
	indexed, head, err := x.Extend(t.Context(), uuid, src, 1)
	if err != nil || indexed != 3 || head != 10 || len(src.calls) != 1 {
		t.Fatalf("indexed=%d head=%d calls=%v err=%v", indexed, head, src.calls, err)
	}
}

func TestExtendRefusesAnInconsistentLogAndKeepsWhatWasGood(t *testing.T) {
	for name, broken := range map[string]func(int64, int64, []Commit) []Commit{
		"gap": func(newest, oldest int64, c []Commit) []Commit {
			if oldest == 4 {
				return c[1:]
			}
			return c
		},
		"out of order": func(newest, oldest int64, c []Commit) []Commit {
			if oldest == 4 {
				c[0], c[1] = c[1], c[0]
			}
			return c
		},
		"no date": func(newest, oldest int64, c []Commit) []Commit {
			if oldest == 4 {
				c[0].Date = ""
			}
			return c
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := &Index{Dir: t.TempDir(), Page: 3}
			src := linearSource(7)
			src.broken = broken
			indexed, _, err := x.Extend(t.Context(), uuid, src, 100)
			if !errors.Is(err, ErrInconsistent) || indexed != 3 {
				t.Fatalf("indexed=%d err=%v", indexed, err)
			}
			if got, _ := x.Indexed(uuid); got != 3 {
				t.Fatalf("index after refusal holds r%d", got)
			}
		})
	}
	x := &Index{Dir: t.TempDir(), Page: 3}
	if _, _, err := x.Extend(t.Context(), uuid, linearSource(5), 100); err != nil {
		t.Fatal(err)
	}
	if _, _, err := x.Extend(t.Context(), uuid, linearSource(4), 100); !errors.Is(err, ErrInconsistent) {
		t.Fatalf("HEAD behind the index accepted: %v", err)
	}
}

func TestAnInterruptedAppendIsNotAnEntry(t *testing.T) {
	x := &Index{Dir: t.TempDir(), Page: 10}
	src := linearSource(4)
	if _, _, err := x.Extend(t.Context(), uuid, src, 100); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(x.Dir, uuid+".ndjson"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"r":5,"t":1`)
	file.Close()
	if got, err := x.Indexed(uuid); err != nil || got != 4 {
		t.Fatalf("Indexed with a torn tail = %d %v", got, err)
	}
	if result, err := x.Aggregate(uuid, Query{BucketSeconds: 86400}); err != nil || result.Indexed != 4 {
		t.Fatalf("aggregate with a torn tail = %+v %v", result, err)
	}
	src.head, src.minutes[5], src.paths[5] = 5, 50, []string{"/five"}
	if indexed, _, err := x.Extend(t.Context(), uuid, src, 100); err != nil || indexed != 5 {
		t.Fatalf("extend after a torn tail: %d %v", indexed, err)
	}
	if result, err := x.Aggregate(uuid, Query{BucketSeconds: 86400}); err != nil || result.Indexed != 5 || result.Buckets[0].Commits != 5 {
		t.Fatalf("aggregate after repair = %+v %v", result, err)
	}
}

func TestAggregateCountsEveryRevisionInLocalBuckets(t *testing.T) {
	src := &fakeSource{
		head: 5,
		minutes: map[int64]int{
			1: 10,         // 00:10 UTC
			2: 20,         // 00:20 UTC
			3: 70,         // 01:10 UTC
			4: 22*60 + 30, // 22:30 UTC = 00:30 next day at +02:00
			5: 23*60 + 50,
		},
		paths: map[int64][]string{
			1: {"/a", "/b"},
			2: {"/a", "/c"},
			3: {"/a"},
			4: {"/d"},
			5: {"/d", "/e", "/f"},
		},
		shouts: map[int64]bool{2: true, 5: true},
	}
	x := &Index{Dir: t.TempDir()}
	if _, _, err := x.Extend(t.Context(), uuid, src, 10); err != nil {
		t.Fatal(err)
	}

	hourly, err := x.Aggregate(uuid, Query{BucketSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	first := hourly.Buckets[0]
	if len(hourly.Buckets) != 4 || first.Start != epoch.Unix() || first.Commits != 2 || first.ChangedPaths != 4 || first.UniquePaths != 3 || first.Shouts != 1 || !first.UniqueExact {
		t.Fatalf("hourly = %+v", hourly.Buckets)
	}
	if hourly.FirstUnix != epoch.Add(10*time.Minute).Unix() || hourly.LastUnix != epoch.Add(23*time.Hour+50*time.Minute).Unix() || hourly.Indexed != 5 {
		t.Fatalf("range = %+v", hourly)
	}

	local, err := x.Aggregate(uuid, Query{BucketSeconds: 86400, OffsetSeconds: 2 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	if len(local.Buckets) != 2 || local.Buckets[0].Commits != 3 || local.Buckets[1].Commits != 2 ||
		local.Buckets[1].Start != epoch.Add(22*time.Hour).Unix() || local.Buckets[1].UniquePaths != 3 {
		t.Fatalf("local days = %+v", local.Buckets)
	}

	window, err := x.Aggregate(uuid, Query{BucketSeconds: 3600, From: epoch.Add(time.Hour), To: epoch.Add(23 * time.Hour)})
	if err != nil || len(window.Buckets) != 2 || window.Buckets[0].Commits != 1 {
		t.Fatalf("window = %+v %v", window.Buckets, err)
	}

	page, err := x.Aggregate(uuid, Query{BucketSeconds: 3600, Limit: 3})
	if err != nil || len(page.Buckets) != 3 || !page.More {
		t.Fatalf("first page = %+v %v", page, err)
	}
	rest, err := x.Aggregate(uuid, Query{BucketSeconds: 3600, Limit: 3, After: page.Buckets[2].Start})
	if err != nil || len(rest.Buckets) != 1 || rest.More || rest.Buckets[0].Start != hourly.Buckets[3].Start {
		t.Fatalf("second page = %+v %v", rest, err)
	}
}

func TestCappedPathsMakeDistinctCountsInexact(t *testing.T) {
	big := make([]string, MaxPathsPerEntry+10)
	for i := range big {
		big[i] = fmt.Sprintf("/reorganised/%d", i)
	}
	src := &fakeSource{head: 1, minutes: map[int64]int{1: 5}, paths: map[int64][]string{1: big}}
	x := &Index{Dir: t.TempDir()}
	if _, _, err := x.Extend(t.Context(), uuid, src, 1); err != nil {
		t.Fatal(err)
	}
	result, err := x.Aggregate(uuid, Query{BucketSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	b := result.Buckets[0]
	if b.ChangedPaths != int64(len(big)) || b.UniquePaths != MaxPathsPerEntry || b.UniqueExact {
		t.Fatalf("bucket = %+v", b)
	}

	// Past the size limit an index keeps counting but stops keeping hashes:
	// r1 lands in an empty file with its hash, r2 and r3 after the limit.
	small := &Index{Dir: t.TempDir(), Page: 1, MaxBytes: 1}
	src2 := linearSource(3)
	if indexed, _, err := small.Extend(t.Context(), uuid, src2, 3); err != nil || indexed != 3 {
		t.Fatalf("indexed=%d err=%v", indexed, err)
	}
	over, err := small.Aggregate(uuid, Query{BucketSeconds: 86400})
	if err != nil || over.Buckets[0].Commits != 3 || over.Buckets[0].ChangedPaths != 3 || over.Buckets[0].UniqueExact {
		t.Fatalf("over the size limit = %+v %v", over.Buckets, err)
	}
}

func TestIndexRefusesBadIdentitiesAndQueries(t *testing.T) {
	x := &Index{Dir: t.TempDir()}
	for _, bad := range []string{"", "../escape", "a/b", strings.Repeat("a", 65)} {
		if _, _, err := x.Extend(t.Context(), bad, linearSource(1), 1); !errors.Is(err, ErrInvalidRepository) {
			t.Errorf("Extend(%q): %v", bad, err)
		}
		if _, err := x.Aggregate(bad, Query{BucketSeconds: 3600}); !errors.Is(err, ErrInvalidRepository) {
			t.Errorf("Aggregate(%q): %v", bad, err)
		}
	}
	for name, q := range map[string]Query{
		"tiny bucket":    {BucketSeconds: 1},
		"zone too far":   {BucketSeconds: 3600, OffsetSeconds: 15 * 3600},
		"negative limit": {BucketSeconds: 3600, Limit: -1},
	} {
		if _, err := x.Aggregate(uuid, q); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	if empty, err := x.Aggregate(uuid, Query{BucketSeconds: 3600}); err != nil || len(empty.Buckets) != 0 || empty.Buckets == nil {
		t.Fatalf("empty index = %+v %v", empty, err)
	}
}
