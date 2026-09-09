package client

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCommitTargetsBounds(t *testing.T) {
	for _, paths := range [][]string{nil, {"."}, {"a", "a"}, {"a\x00b"}, {"\xff"}, {"../a"}, {"a\nb"}, {"a\x1ab"}, {".filees/cache"}, {strings.Repeat("a", nativeCommitTargetBytes)}} {
		if _, err := nativeCommitTargets(paths); err == nil {
			t.Fatalf("accepted invalid target set (%d)", len(paths))
		}
	}
	paths := make([]string, nativeCommitTargetLimit)
	for i := range paths {
		paths[i] = fmt.Sprint(i)
	}
	if _, err := nativeCommitTargets(paths); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeCommitTargets(append(paths, "extra")); err == nil {
		t.Fatal("count limit ignored")
	}
	input, err := nativeCommitTargets([]string{strings.Repeat("a", nativeCommitTargetBytes-1)})
	if err != nil || len(input) != nativeCommitTargetBytes || input[len(input)-1] != 0 {
		t.Fatal("exact byte boundary", err)
	}
}

func TestNativeBatchesBoundUnicodeArgumentsWithoutDroppingPaths(t *testing.T) {
	paths := make([]string, 1200)
	for i := range paths {
		paths[i] = fmt.Sprintf("%04d/%s", i, strings.Repeat("新😀", 100))
	}
	batches := nativeBatches(paths)
	cursor := 0
	for _, batch := range batches {
		units := 0
		if len(batch) > nativePathBatch {
			t.Fatal("count overflow")
		}
		for _, path := range batch {
			units += nativeArgumentUnits(path)
			if path != paths[cursor] {
				t.Fatal("reordered/lost path")
			}
			cursor++
		}
		if units > 12000 {
			t.Fatal("argument overflow", units)
		}
	}
	if cursor != len(paths) {
		t.Fatal("lost targets", cursor)
	}
}

func TestNativeCommitLargeTargetsSingleInvocation(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":7}`)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	paths := make([]string, 1200)
	for i := range paths {
		paths[i] = fmt.Sprintf("新/%04d-%s.txt", i, strings.Repeat("long", 20))
	}
	_, rev, err := c.nativeCommit(t.Context(), t.TempDir(), paths, "one transaction", "marker", false)
	if err != nil || rev != 7 {
		t.Fatal(rev, err)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte(`"commit"`)) != 1 || !bytes.Contains(raw, []byte(`--targets-stdin`)) || !bytes.Contains(raw, []byte(paths[len(paths)-1])) {
		t.Fatal("split or missing targets")
	}
	t.Setenv("FILEES_TEST_RA_OLD_HELPER", "1")
	if _, _, err := c.nativeCommit(t.Context(), t.TempDir(), paths, "refuse", "", false); err == nil {
		t.Fatal("old helper accepted")
	}
	after, _ := os.ReadFile(trace)
	if !bytes.Equal(raw, after) {
		t.Fatal("old helper performed work")
	}
}
