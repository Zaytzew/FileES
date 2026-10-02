//go:build native_svn_probe && windows

package commit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/client"
	"filees/pkg/watcher"
)

// A versioned file renamed twice and removed before publication must publish
// only the removal of its original path. Exercise both the live event and the
// uncertain queue left by an older build against the real Windows SVN helper.
func TestNativeRenameChainThenRemoveWindows(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	for _, stale := range []bool{false, true} {
		name := "delete event"
		if stale {
			name = "stale uncertain queue"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
			svn := func(dir string, args ...string) string {
				t.Helper()
				cmd := exec.Command("svn", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("svn %v: %v\n%s", args, err, out)
				}
				return string(out)
			}
			if out, err := exec.Command("svnadmin", "create", repo).CombinedOutput(); err != nil {
				t.Fatalf("svnadmin: %v\n%s", err, out)
			}
			repoURL := svnurl.File(repo)
			svn(root, "checkout", "--quiet", repoURL, wc)
			for _, p := range []string{".filees/state", ".filees/commit_cache", ".filees/passports"} {
				if err := os.MkdirAll(filepath.Join(wc, p), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"old.txt", "untouched.txt"} {
				if err := os.WriteFile(filepath.Join(wc, name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			svn(wc, "add", "--quiet", "old.txt", "untouched.txt")
			svn(wc, "commit", "--quiet", "-m", "base")

			s := &Service{
				Cli:     client.New(client.Options{NativeSVNPath: helper, Timeout: 10 * time.Second}),
				RepoURL: repoURL, wc: wc, repoID: "native-fixture", UUID: "native-fixture",
				Rules:   Rules{MaxBatchFiles: 100, MaxBatchBytes: 1024 * 1024},
				staging: map[string]*stageItem{}, cachePath: filepath.Join(wc, ".filees/commit_cache/cache.json"),
			}
			for _, step := range [][2]string{{"old.txt", "middle.txt"}, {"middle.txt", "final.txt"}} {
				if err := os.Rename(filepath.Join(wc, step[0]), filepath.Join(wc, step[1])); err != nil {
					t.Fatal(err)
				}
				s.addEvent(watcher.Event{Path: filepath.Join(wc, step[1]), Rel: step[1], OldRel: step[0], Op: watcher.Renamed, Type: watcher.EntryFile, IdentityVerified: true})
			}
			if err := os.Remove(filepath.Join(wc, "final.txt")); err != nil {
				t.Fatal(err)
			}
			if stale {
				if s.staging["final.txt"] == nil {
					t.Fatalf("rename chain missing from queue: %+v", s.staging)
				}
				s.staging["final.txt"].Op = watcher.RenameUncertain
				s.saveCache()
			} else {
				s.addEvent(watcher.Event{Path: filepath.Join(wc, "final.txt"), Rel: "final.txt", Op: watcher.Deleted, Type: watcher.EntryFile})
			}
			if err := s.tryCommitMode(context.Background(), wc, true); err != nil {
				t.Fatal(err)
			}
			if len(s.staging) != 0 {
				t.Fatalf("queue not drained: %+v", s.staging)
			}
			listing := svn(wc, "list", repoURL)
			if strings.Contains(listing, "old.txt") || strings.Contains(listing, "middle.txt") || strings.Contains(listing, "final.txt") || !strings.Contains(listing, "untouched.txt") {
				t.Fatalf("repository after rename chain and removal: %q", listing)
			}
		})
	}
}
