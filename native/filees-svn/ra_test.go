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
