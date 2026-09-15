//go:build native_svn_probe

package nativesvnprobe

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Wehikuł czasu reads the repository as it was. Both verbs take an explicit
// revision, which is also the peg: the name means the object that lived there
// then, including folders a later reorganisation removed.

func historyEntries(t *testing.T, got map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := got["entries"].([]any)
	if !ok {
		t.Fatalf("no entries: %v", got)
	}
	out := map[string]map[string]any{}
	var order []string
	for _, item := range raw {
		entry := item.(map[string]any)
		name := entry["name"].(string)
		order = append(order, name)
		out[name] = entry
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] > order[i] {
			t.Fatalf("entries not sorted by name: %v", order)
		}
	}
	return out
}

func TestHistoryListShowsTheTreeAsItWasAtARevision(t *testing.T) {
	f := newFixture(t, "old.txt") // r1: old.txt, occupied.txt, folder/
	write(t, filepath.Join(f.wc, "folder", "inside.txt"), "kept in the past\n")
	f.svnRun(t, "add", "folder/inside.txt")
	f.svnRun(t, "commit", "--username", "editor", "-m", "fill folder") // r2
	// The folder node is still at r1 while its child is at r2; SVN refuses to
	// delete a mixed-revision directory, so bring it up to date first.
	f.svnRun(t, "update")
	f.svnRun(t, "delete", "folder")
	f.svnRun(t, "commit", "--username", "editor", "-m", "reorganise") // r3

	past := historyEntries(t, f.jsonCall(t, true, "list", "--url", f.repoURL, "--revision", "2"))
	if past["folder"]["kind"] != "dir" || past["folder"]["size"] != nil {
		t.Fatalf("folder at r2: %v", past["folder"])
	}
	if past["occupied.txt"]["kind"] != "file" || past["occupied.txt"]["size"] != float64(len("another object\n")) {
		t.Fatalf("occupied.txt at r2: %v", past["occupied.txt"])
	}
	if past["occupied.txt"]["last_changed_revision"] != float64(1) || past["occupied.txt"]["last_author"] != "creator" {
		t.Fatalf("a file untouched since r1 must report r1: %v", past["occupied.txt"])
	}

	now := historyEntries(t, f.jsonCall(t, true, "list", "--url", f.repoURL, "--revision", "3"))
	if _, still := now["folder"]; still {
		t.Fatal("folder removed in r3 still listed at r3")
	}

	inside := historyEntries(t, f.jsonCall(t, true, "list", "--url", f.repoURL+"/folder", "--revision", "2"))
	entry := inside["inside.txt"]
	if entry == nil || entry["last_changed_revision"] != float64(2) || entry["last_author"] != "editor" {
		t.Fatalf("a folder gone from HEAD must stay listable at its own revision: %v", inside)
	}
	if date, _ := entry["last_changed_date"].(string); !strings.HasPrefix(date, "20") {
		t.Fatalf("last_changed_date = %v", entry["last_changed_date"])
	}

	f.jsonCall(t, false, "list", "--url", f.repoURL+"/folder", "--revision", "3")
}

// The reason fetch-file exists. The repository stores a native file with LF
// and keywords contracted; the copy must be those bytes, not a rendering.
func TestHistoryFetchFileReturnsRepositoryBytes(t *testing.T) {
	f := newFixture(t, "old.txt")
	want := "first line\nId: $Id$\n"
	write(t, filepath.Join(f.wc, "text.txt"), want)
	f.svnRun(t, "add", "text.txt")
	f.svnRun(t, "propset", "svn:eol-style", "native", "text.txt")
	f.svnRun(t, "propset", "svn:keywords", "Id", "text.txt")
	f.svnRun(t, "commit", "--username", "editor", "-m", "translated file") // r2

	out := filepath.Join(f.root, "raw.txt")
	got := f.jsonCall(t, true, "fetch-file", "--url", f.repoURL+"/text.txt", "--revision", "2", "--out", out)
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Fatalf("fetch-file content = %q, want repository bytes %q", content, want)
	}
	if got["bytes"] != float64(len(want)) || got["revision"] != float64(2) {
		t.Fatalf("receipt = %v", got)
	}

	// cat keeps its keyword guard but still translates line endings; on
	// Windows that is visible as CRLF. This pins why history does not use it.
	if runtime.GOOS == "windows" {
		viaCat := filepath.Join(f.root, "cat.txt")
		f.jsonCall(t, true, "cat", "--url", f.repoURL+"/text.txt", "--out", viaCat, "--revision", "2")
		translated, err := os.ReadFile(viaCat)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(translated), "\r\n") {
			t.Fatalf("cat no longer translates EOL (%q); revisit fetch-file's reason", translated)
		}
	}
}

func TestHistoryFetchFileReadsPathsGoneFromHead(t *testing.T) {
	f := newFixture(t, "old.txt") // r1: occupied.txt = "another object\n"
	f.svnRun(t, "move", "occupied.txt", "renamed.txt")
	f.svnRun(t, "commit", "--username", "editor", "-m", "rename") // r2
	f.svnRun(t, "delete", "renamed.txt")
	f.svnRun(t, "commit", "--username", "editor", "-m", "remove") // r3

	for _, tc := range []struct{ path, revision string }{
		{"occupied.txt", "1"},
		{"renamed.txt", "2"},
	} {
		out := filepath.Join(f.root, tc.path+"@"+tc.revision)
		f.jsonCall(t, true, "fetch-file", "--url", f.repoURL+"/"+tc.path, "--revision", tc.revision, "--out", out)
		if content, err := os.ReadFile(out); err != nil || string(content) != "another object\n" {
			t.Fatalf("%s@%s = %q %v", tc.path, tc.revision, content, err)
		}
	}

	for _, tc := range []struct{ path, revision string }{
		{"renamed.txt", "3"},  // deleted in r3
		{"occupied.txt", "2"}, // moved away in r2
	} {
		out := filepath.Join(f.root, "absent-"+tc.path+"-"+tc.revision)
		f.jsonCall(t, false, "fetch-file", "--url", f.repoURL+"/"+tc.path, "--revision", tc.revision, "--out", out)
		for _, leftover := range []string{out, out + ".part"} {
			if _, err := os.Stat(leftover); !os.IsNotExist(err) {
				t.Fatalf("%s survived a refused fetch (err=%v)", leftover, err)
			}
		}
	}
}

func TestHistoryReadsRefuseUnsafeOrAmbiguousArguments(t *testing.T) {
	f := newFixture(t, "old.txt")
	taken := filepath.Join(f.root, "taken.txt")
	write(t, taken, "already here\n")
	fresh := func(name string) string { return filepath.Join(f.root, name) }

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"list without revision", []string{"list", "--url", f.repoURL}},
		{"fetch without revision", []string{"fetch-file", "--url", f.repoURL + "/occupied.txt", "--out", fresh("a.txt")}},
		{"fetch onto an existing file", []string{"fetch-file", "--url", f.repoURL + "/occupied.txt", "--revision", "1", "--out", taken}},
		{"fetch to a relative path", []string{"fetch-file", "--url", f.repoURL + "/occupied.txt", "--revision", "1", "--out", "relative.txt"}},
		{"fetch a directory", []string{"fetch-file", "--url", f.repoURL + "/folder", "--revision", "1", "--out", fresh("b.txt")}},
		{"list a file", []string{"list", "--url", f.repoURL + "/occupied.txt", "--revision", "1"}},
		{"local path instead of URL", []string{"list", "--url", f.wc, "--revision", "1"}},
		{"negative revision", []string{"list", "--url", f.repoURL, "--revision", "-1"}},
		{"unknown flag", []string{"list", "--url", f.repoURL, "--revision", "1", "--depth", "infinity"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.jsonCall(t, false, tc.args...)
		})
	}
	if content, err := os.ReadFile(taken); err != nil || string(content) != "already here\n" {
		t.Fatalf("a refused fetch touched the target: %q %v", content, err)
	}

}

// A symbolic link is stored as "link TARGET" text. Written out as a file it
// would silently become data, so it is refused until the concept decides the
// export policy for special nodes.
func TestHistoryFetchFileRefusesSpecialNodes(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "link"), "link occupied.txt")
	f.svnRun(t, "add", "link")
	// SVN stores any value of svn:special as "*". Passing "*" itself does not
	// work on Windows: the C runtime of svn.exe expands it into file names.
	f.svnRun(t, "propset", "svn:special", "on", "link")
	f.svnRun(t, "commit", "--username", "editor", "-m", "special") // r2
	out := filepath.Join(f.root, "link.out")
	f.jsonCall(t, false, "fetch-file", "--url", f.repoURL+"/link", "--revision", "2", "--out", out)
	for _, leftover := range []string{out, out + ".part"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatalf("%s was written for a special node (err=%v)", leftover, err)
		}
	}
}
