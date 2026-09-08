//go:build native_svn_probe

package nativesvnprobe

import (
	"os"
	"path/filepath"
	"testing"
)

// cat is the first verb with no working copy. Everything it touches is either a
// URL or the single absolute path it was told to write.
func TestRACatWritesTheRepositoryFile(t *testing.T) {
	f := newFixture(t, "old.txt")
	out := filepath.Join(f.root, "fetched.txt")

	got := f.jsonCall(t, true, "cat", "--url", f.repoURL+"/occupied.txt", "--out", out)
	if got["bytes"].(float64) != float64(len("another object\n")) {
		t.Fatalf("bytes = %v", got["bytes"])
	}
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "another object\n" {
		t.Fatalf("content = %q", content)
	}
}

// A revision must be reachable, or release material can only ever be fetched
// from HEAD - which is not a pin at all.
func TestRACatHonoursARevision(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "occupied.txt"), "second version\n")
	f.svnRun(t, "commit", "--username", "editor", "-m", "second")

	first := filepath.Join(f.root, "r1.txt")
	f.jsonCall(t, true, "cat", "--url", f.repoURL+"/occupied.txt", "--out", first, "--revision", "1")
	content, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "another object\n" {
		t.Fatalf("revision 1 content = %q", content)
	}
}

// The guards. Each one is a way for a mistyped argument to become damage, and
// none of them can be checked by the .filees marker the working-copy verbs use.
func TestRACatRefusesUnsafeArguments(t *testing.T) {
	f := newFixture(t, "old.txt")
	occupied := filepath.Join(f.root, "taken.txt")
	write(t, occupied, "already here\n")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"target exists", []string{"cat", "--url", f.repoURL + "/occupied.txt", "--out", occupied}},
		{"relative target", []string{"cat", "--url", f.repoURL + "/occupied.txt", "--out", "relative.txt"}},
		{"target is a local path, not a URL", []string{"cat", "--url", f.wc, "--out", filepath.Join(f.root, "x.txt")}},
		{"no url", []string{"cat", "--out", filepath.Join(f.root, "y.txt")}},
		{"negative revision", []string{"cat", "--url", f.repoURL + "/occupied.txt", "--out", filepath.Join(f.root, "z.txt"), "--revision", "-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.jsonCall(t, false, tc.args...)
		})
	}
	if content, err := os.ReadFile(occupied); err != nil || string(content) != "already here\n" {
		t.Fatalf("refused fetch still touched the target: %q %v", content, err)
	}
}

// A failed fetch must leave nothing behind. Without cleanup the ".part" file
// survives and the next attempt fails on the exclusive open instead of on the
// real reason - a retry that reports the wrong problem.
func TestRACatLeavesNoPartialAfterFailure(t *testing.T) {
	f := newFixture(t, "old.txt")
	out := filepath.Join(f.root, "missing.txt")

	f.jsonCall(t, false, "cat", "--url", f.repoURL+"/no-such-file.txt", "--out", out)

	for _, path := range []string{out, out + ".part"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived a failed fetch (err=%v)", path, err)
		}
	}
	// And the retry reports the repository, not the leftover.
	f.jsonCall(t, false, "cat", "--url", f.repoURL+"/no-such-file.txt", "--out", out)
}

// log replaces three CLI invocations that were one question asked with
// different fields: the shout inbox wants revision and message, the commit
// receipt lookup wants a named revprop, and move-result recovery wants changed
// paths with copyfrom.
func TestRALogReadsRevisionsAndMessages(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "occupied.txt"), "second\n")
	f.svnRun(t, "commit", "--username", "editor", "-m", "second commit")

	got := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "HEAD:1")
	entries, _ := got["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", got["entries"])
	}
	newest := entries[0].(map[string]any)
	if newest["revision"].(float64) != 2 || newest["message"] != "second commit" {
		t.Fatalf("newest entry = %#v", newest)
	}
	if newest["author"] != "editor" {
		t.Fatalf("author = %#v", newest["author"])
	}
	// Both entries must carry their own strings. The receiver's scratch pool is
	// cleared between entries, so a kept pointer reads freed memory - measured
	// 2026-09-08, when the newest entry came back with the tail of another
	// entry's date as its author.
	oldest := entries[1].(map[string]any)
	if oldest["message"] != "birth" || oldest["author"] != "creator" {
		t.Fatalf("oldest entry was corrupted by the newer one: %#v", oldest)
	}
}

func TestRALogReportsChangedPathsWithCopyfrom(t *testing.T) {
	f := newFixture(t, "old.txt")
	f.svnRun(t, "copy", "--", "occupied.txt", "copied.txt")
	f.svnRun(t, "commit", "--username", "copier", "-m", "copy with history")

	got := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "2", "--changed-paths")
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", got["entries"])
	}
	paths, _ := entries[0].(map[string]any)["paths"].([]any)
	var found bool
	for _, raw := range paths {
		p := raw.(map[string]any)
		if p["path"] == "/copied.txt" {
			found = true
			if p["action"] != "A" || p["copyfrom_path"] != "/occupied.txt" || p["copyfrom_rev"].(float64) != 1 {
				t.Fatalf("copy not described: %#v", p)
			}
		}
	}
	if !found {
		t.Fatalf("copied path missing: %#v", paths)
	}
}

func TestRALogReturnsNamedRevprops(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "occupied.txt"), "receipted\n")
	f.svnRun(t, "commit", "--username", "editor", "--with-revprop", "filees:commit-id=RECEIPT-1", "-m", "with receipt")

	got := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "2", "--revprop", "filees:commit-id")
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", got["entries"])
	}
	props, _ := entries[0].(map[string]any)["revprops"].(map[string]any)
	if props["filees:commit-id"] != "RECEIPT-1" {
		t.Fatalf("revprops = %#v", props)
	}
}

func TestRALogAcceptsAWorkingCopyTarget(t *testing.T) {
	f := newFixture(t, "old.txt")
	got := f.jsonCall(t, true, "log", "--disposable-wc", f.wc, "--revision", "1", "--", "occupied.txt")
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["revision"].(float64) != 1 {
		t.Fatalf("entries = %#v", got["entries"])
	}
}

func TestRALogRefusesAmbiguousOrUnboundedRequests(t *testing.T) {
	f := newFixture(t, "old.txt")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"both sources", []string{"log", "--url", f.repoURL, "--disposable-wc", f.wc, "--revision", "1"}},
		{"no source", []string{"log", "--revision", "1"}},
		{"no revision", []string{"log", "--url", f.repoURL}},
		{"working copy without a target", []string{"log", "--disposable-wc", f.wc, "--revision", "1"}},
		{"url with a separate target", []string{"log", "--url", f.repoURL, "--revision", "1", "--", "occupied.txt"}},
		{"nonsense revision", []string{"log", "--url", f.repoURL, "--revision", "yesterday"}},
	} {
		t.Run(tc.name, func(t *testing.T) { f.jsonCall(t, false, tc.args...) })
	}
}
