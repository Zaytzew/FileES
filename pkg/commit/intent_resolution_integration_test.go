//go:build native_svn_probe

package commit

import (
	"context"
	"filees/internal/svnurl"
	"filees/pkg/client"
	"filees/pkg/watcher"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// An actual local repository, two differently named files and a service restart.
// svnadmin/svnlook are fixture/verification tools only; all client operations on
// Windows go through the supplied native helper with an absent fallback path.
func TestIntentResolutionRealPublication(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	run := func(tool string, args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(t.Context(), tool, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", tool, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	run("svnadmin", "create", repo)
	url := svnurl.File(repo)
	opts := client.Options{Timeout: 20 * time.Second}
	if runtime.GOOS == "windows" {
		opts.NativeSVNPath = os.Getenv("FILEES_SVN_PROBE")
		if !filepath.IsAbs(opts.NativeSVNPath) {
			t.Fatal("FILEES_SVN_PROBE required")
		}
		opts.SvnPath = filepath.Join(root, "absent-cli")
	}
	cli := client.New(opts)
	if _, err := cli.Checkout(t.Context(), url, wc); err != nil {
		t.Fatal(err)
	}
	// WC-local native operations require the product's metadata directory.
	if err := os.MkdirAll(filepath.Join(wc, ".filees", "state"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wc, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("old.txt", "old version")
	t.Logf("WC=%s", wc)
	if info, err := os.Stat(filepath.Join(wc, ".svn")); err != nil || !info.IsDir() {
		t.Fatalf("checkout metadata: %v", err)
	}
	if _, err := cli.Add(t.Context(), wc, []string{"old.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cli.(interface {
		CommitWithRevision(context.Context, string, string, []string, string, bool) (string, int64, error)
	}).CommitWithRevision(t.Context(), wc, url, []string{"old.txt"}, "seed", false); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"state", "commit_cache"} {
		if err := os.MkdirAll(filepath.Join(wc, ".filees", dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(wc, "old.txt")); err != nil {
		t.Fatal(err)
	}
	write("new V2.txt", "replacement with a different name")
	newService := func() *Service {
		return &Service{Cli: cli, RepoURL: url, repoID: "intent-live", wc: wc, RequireSVNMetadata: true, Rules: Rules{NewLatency: time.Nanosecond, MaxBatchFiles: 100}, staging: map[string]*stageItem{}, cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json")}
	}
	s := newService()
	s.addEvent(watcher.Event{Op: watcher.Deleted, Type: watcher.EntryFile, Rel: "old.txt", Path: filepath.Join(wc, "old.txt")})
	ev := watcher.Event{Op: watcher.RenameUncertain, Type: watcher.EntryFile, Rel: "new V2.txt", Path: filepath.Join(wc, "new V2.txt")}
	s.addEvent(ev)
	if err := s.saveCacheChecked(); err != nil {
		t.Fatal(err)
	}
	if err := s.tryCommit(t.Context(), wc); err == nil {
		t.Fatal("published ambiguity without decision")
	}
	plan, err := s.PlanIntents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyIntents(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	s = newService()
	s.loadCache()
	s.addEvent(ev)
	if err := s.tryCommit(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if run("svnlook", "youngest", repo) != "2" {
		t.Fatal("expected exactly one publication")
	}
	if got := run("svnlook", "cat", repo, "new V2.txt"); got != "replacement with a different name" {
		t.Fatal(got)
	}
	changed := run("svnlook", "changed", "--copy-info", "-r", "2", repo)
	if !strings.Contains(changed, "old.txt") || !strings.Contains(changed, "new V2.txt") || strings.Contains(changed, "(from ") {
		t.Fatalf("not independent delete/add: %s", changed)
	}
	if len(s.staging) != 0 {
		t.Fatalf("queue not cleared: %+v", s.staging)
	}
	if err := s.saveCacheChecked(); err != nil {
		t.Fatal(err)
	}
	s = newService()
	s.loadCache()
	if _, err := s.ApplyIntents(t.Context(), plan.PlanID, plan.Choice); err != nil {
		t.Fatal(err)
	}
	if err := s.tryCommit(t.Context(), wc); err != nil {
		t.Fatal(err)
	}
	if run("svnlook", "youngest", repo) != "2" {
		t.Fatal("receipt retry repeated publication")
	}
}
