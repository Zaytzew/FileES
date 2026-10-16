//go:build native_svn_probe && windows

package commit

import (
	"context"
	"filees/internal/svnurl"
	"filees/pkg/client"
	"filees/pkg/watcher"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeAnchorCopiedTreeMergesWithoutPublishingRemotePathsAsAdds(t *testing.T) {
	t.Run("copied", func(t *testing.T) { testNativeAnchorMerge(t, false, false) })
	t.Run("already-scheduled", func(t *testing.T) { testNativeAnchorMerge(t, true, false) })
	t.Run("remote-edit-after-base", func(t *testing.T) { testNativeAnchorMerge(t, false, true) })
}

func TestNativeAnchorFirstPublicationFromZero(t *testing.T) {
	for _, collision := range []bool{false, true} {
		name := "first-publication"
		if collision {
			name = "newer-head-collision"
		}
		t.Run(name, func(t *testing.T) {
			helper := os.Getenv("FILEES_SVN_PROBE")
			if helper == "" {
				t.Fatal("FILEES_SVN_PROBE required")
			}
			root := t.TempDir()
			t.Cleanup(func() {
				_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
					if err == nil && e.Type().IsRegular() {
						_ = os.Chmod(p, 0600)
					}
					return nil
				})
			})
			run := func(dir, bin string, args ...string) string {
				t.Helper()
				c := exec.CommandContext(t.Context(), bin, args...)
				c.Dir = dir
				out, err := c.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v: %v %s", bin, args, err, out)
				}
				return string(out)
			}
			repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
			run(root, "svnadmin", "create", repo)
			uri := svnurl.File(repo)
			run(root, "svn", "checkout", "--quiet", "--depth", "empty", uri, wc)
			for _, rel := range []string{".filees/state", ".filees/commit_cache"} {
				if err := os.MkdirAll(filepath.Join(wc, rel), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(p, body string) {
				t.Helper()
				if err := os.WriteFile(p, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(wc, "new.txt"), "local bytes")
			if collision {
				other := filepath.Join(root, "other")
				run(root, "svn", "checkout", "--quiet", uri, other)
				write(filepath.Join(other, "new.txt"), "newer remote bytes")
				run(other, "svn", "add", "--quiet", "new.txt")
				run(other, "svn", "commit", "--quiet", "-m", "remote addition")
			}
			cli := client.New(client.Options{NativeSVNPath: helper, Timeout: 15 * time.Second})
			s := &Service{Anchor: true, AnchorMergeRevision: func() (int64, error) { return 0, nil }, Cli: cli, RepoURL: uri, wc: wc, repoID: "zero-fixture", UUID: "zero-fixture", Rules: Rules{MaxBatchFiles: 100, MaxBatchBytes: 1 << 20}, staging: map[string]*stageItem{}, cachePath: filepath.Join(wc, ".filees/commit_cache/cache.json")}
			s.addEvent(watcher.Event{Rel: "new.txt", Path: filepath.Join(wc, "new.txt"), Op: watcher.Added, Type: watcher.EntryFile})
			err := s.tryCommitMode(t.Context(), wc, true)
			if collision {
				if err == nil {
					t.Fatal("newer remote addition overwritten")
				}
				if got := run(root, "svn", "cat", uri+"/new.txt"); got != "newer remote bytes" {
					t.Fatal(got)
				}
			} else {
				if err != nil {
					t.Fatal("first publication refused", err)
				}
				if got := run(root, "svn", "cat", uri+"/new.txt"); got != "local bytes" {
					t.Fatal(got)
				}
				if len(s.staging) != 0 {
					t.Fatal("published item still queued")
				}
				if err := s.tryCommitMode(t.Context(), wc, true); err != nil {
					t.Fatal(err)
				}
			}
			if got := strings.TrimSpace(run(root, "svnlook", "youngest", repo)); got != "1" {
				t.Fatal("unexpected commit", got)
			}
			if got, err := os.ReadFile(filepath.Join(wc, "new.txt")); err != nil || string(got) != "local bytes" {
				t.Fatal("local bytes changed", err)
			}
			plan := filepath.Join(root, "zero.jsonl")
			summary, err := cli.(client.HistoryTreeReader).HistoryListTree(t.Context(), uri, 0, plan)
			if err != nil || summary.Revision != 0 {
				t.Fatal("zero became HEAD", summary, err)
			}
			if raw, err := os.ReadFile(plan); err != nil || len(strings.TrimSpace(string(raw))) != 0 {
				t.Fatal("nonempty revision zero", string(raw), err)
			}
		})
	}
}

func testNativeAnchorMerge(t *testing.T, scheduled, remoteEdit bool) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(p string, entry os.DirEntry, err error) error {
			if err == nil && entry.Type().IsRegular() {
				_ = os.Chmod(p, 0600) // SVN's readonly fixture pristines, not product data.
			}
			return nil
		})
	})
	repo := filepath.Join(root, "repo")
	seed := filepath.Join(root, "seed")
	wc := filepath.Join(root, "anchor")
	run := func(dir, bin string, args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), bin, args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v %s", bin, args, err, out)
		}
		return string(out)
	}
	run(root, "svnadmin", "create", repo)
	uri := svnurl.File(repo)
	run(root, "svn", "checkout", "--quiet", uri, seed)
	write := func(base, rel, body string) {
		t.Helper()
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(seed, "folder/same.txt", "same bytes")
	write(seed, "folder/edited.txt", "remote old")
	write(seed, "folder/untouched.txt", "never download")
	run(seed, "svn", "add", "--quiet", "folder")
	run(seed, "svn", "commit", "--quiet", "-m", "base")
	run(root, "svn", "checkout", "--quiet", "--depth", "empty", uri, wc)
	for _, p := range []string{".filees/state", ".filees/commit_cache"} {
		if err := os.MkdirAll(filepath.Join(wc, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(wc, "folder/same.txt", "same bytes")
	write(wc, "folder/edited.txt", "copied local edit")
	write(wc, "folder/new.txt", "new bytes")
	if scheduled {
		run(wc, "svn", "add", "--quiet", "folder")
	}
	if remoteEdit {
		write(seed, "folder/edited.txt", "newer remote edit")
		run(seed, "svn", "commit", "--quiet", "-m", "remote changed after anchor base")
	}
	s := &Service{Anchor: true, AnchorMergeRevision: func() (int64, error) { return 1, nil }, Cli: client.New(client.Options{NativeSVNPath: helper, Timeout: 15 * time.Second}), RepoURL: uri, wc: wc, repoID: "merge-fixture", UUID: "merge-fixture", Rules: Rules{MaxBatchFiles: 100, MaxBatchBytes: 1 << 20}, staging: map[string]*stageItem{}, cachePath: filepath.Join(wc, ".filees/commit_cache/cache.json")}
	for _, rel := range []string{"folder/same.txt", "folder/edited.txt", "folder/new.txt"} {
		s.addEvent(watcher.Event{Rel: rel, Path: filepath.Join(wc, filepath.FromSlash(rel)), Op: watcher.Added, Type: watcher.EntryFile})
	}
	if err := s.tryCommitMode(t.Context(), wc, true); err != nil {
		if remoteEdit {
			if got := run(root, "svn", "cat", uri+"/folder/edited.txt"); got != "newer remote edit" {
				t.Fatal("remote edit overwritten", got)
			}
			if got, err := os.ReadFile(filepath.Join(wc, "folder/edited.txt")); err != nil || string(got) != "copied local edit" {
				t.Fatal("local edit lost", err)
			}
			if got := strings.TrimSpace(run(root, "svnlook", "youngest", repo)); got != "2" {
				t.Fatal("unexpected publication", got)
			}
			return
		}
		t.Fatalf("merge publication failed: %v", err)
	}
	if remoteEdit {
		t.Fatal("newer remote edit was accepted as a new baseline")
	}
	if got := run(root, "svn", "cat", uri+"/folder/edited.txt"); got != "copied local edit" {
		t.Fatal("local edit lost", got)
	}
	if got := run(root, "svn", "cat", uri+"/folder/new.txt"); got != "new bytes" {
		t.Fatal(got)
	}
	log := run(root, "svn", "log", "--xml", "--verbose", "-r", "HEAD", uri)
	if strings.Contains(log, "/folder/same.txt") || !strings.Contains(log, "action=\"M\"") || !strings.Contains(log, "/folder/new.txt") {
		t.Fatal(log)
	}
	if _, err := os.Lstat(filepath.Join(wc, "folder/untouched.txt")); !os.IsNotExist(err) {
		t.Fatal("unselected remote file downloaded", err)
	}
	if err := s.tryCommitMode(t.Context(), wc, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(run(root, "svnlook", "youngest", repo)); got != "2" {
		t.Fatal("duplicate commit", got)
	}
	for rel, body := range map[string]string{"folder/same.txt": "same bytes", "folder/edited.txt": "copied local edit", "folder/new.txt": "new bytes"} {
		raw, err := os.ReadFile(filepath.Join(wc, filepath.FromSlash(rel)))
		if err != nil || string(raw) != body {
			t.Fatal("local bytes changed", rel, err)
		}
	}
}

func TestNativePlainAddRetirementNeverRevertsBaseOrCopyAncestry(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				_ = os.Chmod(p, 0600)
			}
			return nil
		})
	})
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	run := func(dir, bin string, args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), bin, args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return string(out)
	}
	run(root, "svnadmin", "create", repo)
	run(root, "svn", "checkout", "--quiet", svnurl.File(repo), wc)
	os.MkdirAll(filepath.Join(wc, ".filees"), 0700)
	os.WriteFile(filepath.Join(wc, "base.txt"), []byte("base"), 0600)
	run(wc, "svn", "add", "base.txt")
	run(wc, "svn", "commit", "--quiet", "-m", "base")
	os.WriteFile(filepath.Join(wc, "base.txt"), []byte("keep edited bytes"), 0600)
	os.WriteFile(filepath.Join(wc, "new.txt"), []byte("keep new bytes"), 0600)
	run(wc, "svn", "add", "new.txt")
	run(wc, "svn", "copy", "base.txt", "copy.txt")
	c := client.New(client.Options{NativeSVNPath: helper, Timeout: 10 * time.Second}).(interface {
		UnschedulePlainAdds(context.Context, string, []string) error
	})
	for _, selection := range [][]string{{"new.txt", "base.txt"}, {"copy.txt"}} {
		if err := c.UnschedulePlainAdds(t.Context(), wc, selection); err == nil {
			t.Fatal("nonplain metadata was retired", selection)
		}
		if got, err := os.ReadFile(filepath.Join(wc, "base.txt")); err != nil || string(got) != "keep edited bytes" {
			t.Fatal("base bytes changed", err)
		}
		if got := run(wc, "svn", "status", "new.txt"); !strings.HasPrefix(got, "A") {
			t.Fatal("partial mutation before complete preflight", got)
		}
	}
	if err := c.UnschedulePlainAdds(t.Context(), wc, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(wc, "new.txt")); err != nil || string(got) != "keep new bytes" {
		t.Fatal("added bytes changed", err)
	}
	if got := run(wc, "svn", "status", "new.txt"); !strings.HasPrefix(got, "?") {
		t.Fatal("add was not retired", got)
	}
}
