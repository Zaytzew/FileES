//go:build native_svn_probe

package nativesvnprobe

import (
	"path/filepath"
	"testing"
)

// Wehikuł czasu turns a chosen moment into a revision by asking log for the
// newest commit not later than it. The helper resolves the date against the
// repository, so the daemon never pages the whole log to find one number.
func TestLogResolvesADatedRevision(t *testing.T) {
	f := newFixture(t, "old.txt") // r1
	write(t, filepath.Join(f.wc, "later.txt"), "second commit\n")
	f.svnRun(t, "add", "later.txt")
	f.svnRun(t, "commit", "--username", "editor", "-m", "second") // r2

	newest := func(moment string) []any {
		t.Helper()
		got := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "{"+moment+"}:0", "--limit", "1")
		entries, ok := got["entries"].([]any)
		if !ok {
			t.Fatalf("no entries: %v", got)
		}
		return entries
	}
	rev := func(entries []any) float64 {
		t.Helper()
		if len(entries) != 1 {
			t.Fatalf("entries = %v", entries)
		}
		return entries[0].(map[string]any)["revision"].(float64)
	}

	if got := rev(newest("2999-01-01T00:00:00.000000Z")); got != 2 {
		t.Fatalf("a moment after HEAD resolved to r%v", got)
	}
	first := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "1")
	date := first["entries"].([]any)[0].(map[string]any)["date"].(string)
	if got := rev(newest(date)); got != 1 {
		t.Fatalf("the exact date of r1 resolved to r%v; the moment is inclusive", got)
	}
	for _, entry := range newest("1990-01-01T00:00:00.000000Z") {
		if r := entry.(map[string]any)["revision"].(float64); r != 0 {
			t.Fatalf("a moment before the first commit resolved to r%v", r)
		}
	}

	for _, bad := range []string{"{}", "{yesterday}:0", "{2026-09-12T10:00:00.000000Z", "2026-09-12T10:00:00.000000Z}"} {
		f.jsonCall(t, false, "log", "--url", f.repoURL, "--revision", bad, "--limit", "1")
	}
}
