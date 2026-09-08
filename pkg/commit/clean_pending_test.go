package commit

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

type cleanPendingClient struct {
	*revisionClient
	during func()
}

func (c *cleanPendingClient) Status(ctx context.Context, wc string, paths []string) ([]client.StatusEntry, error) {
	if c.during != nil {
		c.during()
	}
	return c.revisionClient.Status(ctx, wc, paths)
}

func TestCleanPendingRequiresPositiveUnchangedFileEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, item, props          string
		op                         watcher.OpType
		fail, absent, mutate, keep bool
	}{
		{name: "downloaded add", item: "normal", props: "none"},
		{name: "downloaded modify", item: "normal", props: "normal", op: watcher.Modified},
		{name: "modified", item: "modified", props: "none", keep: true},
		{name: "properties", item: "normal", props: "modified", keep: true},
		{name: "unknown properties", item: "normal", keep: true},
		{name: "unversioned", item: "unversioned", props: "none", keep: true},
		{name: "missing collision", item: "missing", props: "none", keep: true},
		{name: "deleted", item: "deleted", props: "none", keep: true},
		{name: "no status", keep: true},
		{name: "failure", item: "normal", props: "none", fail: true, keep: true},
		{name: "physical absence", item: "normal", props: "none", absent: true, keep: true},
		{name: "changed during status", item: "normal", props: "none", mutate: true, keep: true},
		{name: "delete intent", item: "normal", props: "none", op: watcher.Deleted, keep: true},
		{name: "rename intent", item: "normal", props: "none", op: watcher.Renamed, keep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wc := t.TempDir()
			path := filepath.Join(wc, "file.txt")
			if !tc.absent {
				if err := os.WriteFile(path, []byte("download"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cli := &cleanPendingClient{revisionClient: &revisionClient{status: []client.StatusEntry{{Path: "file.txt", Item: tc.item, Props: tc.props}}}}
			if tc.fail {
				cli.statusErr = errors.New("status unavailable")
			}
			if tc.mutate {
				cli.during = func() {
					if err := os.WriteFile(path, []byte("a new local edit"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			recorder := &activityRecorder{}
			s := &Service{Cli: cli, wc: wc, repoID: "repo", Activity: recorder, staging: make(map[string]*stageItem), cachePath: filepath.Join(wc, "cache.json")}
			s.acceptEvent(watcher.Event{Rel: "file.txt", Path: path, Type: watcher.EntryFile, Op: tc.op})
			s.wcOpMu.Lock()
			s.reconcileCleanPending(t.Context(), wc)
			s.wcOpMu.Unlock()
			if got := s.stagingLen() != 0; got != tc.keep {
				t.Fatalf("pending=%v want %v", got, tc.keep)
			}
			if !tc.keep {
				if len(recorder.entries) != 0 {
					t.Fatal("stale pending journal")
				}
				b, err := os.ReadFile(s.cachePath)
				if err != nil || strings.TrimSpace(string(b)) != "[]" {
					t.Fatalf("durable cache %s: %v", b, err)
				}
			}
		})
	}
}

func TestDownloadedPendingClearsAtUnchangedHeadRealSVN(t *testing.T) {
	for _, bin := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s absent", bin)
		}
	}
	run := func(bin string, args ...string) {
		t.Helper()
		if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", bin, args, err, out)
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	a := filepath.Join(root, "author")
	b := filepath.Join(root, "reader")
	p := filepath.ToSlash(repo)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	repoURL := (&url.URL{Scheme: "file", Path: p}).String()
	run("svnadmin", "create", repo)
	run("svn", "checkout", repoURL, a)
	run("svn", "checkout", repoURL, b)
	if err := os.WriteFile(filepath.Join(a, "fresh.txt"), []byte("remote content"), 0600); err != nil {
		t.Fatal(err)
	}
	run("svn", "add", filepath.Join(a, "fresh.txt"))
	run("svn", "commit", "-m", "fixture", a)
	run("svn", "update", b)
	s := &Service{Cli: client.New(client.Options{Timeout: 10 * time.Second}), RepoURL: repoURL, wc: b, staging: make(map[string]*stageItem), Logger: talk.With("clean-pending")}
	s.acceptEvent(watcher.Event{Rel: "fresh.txt", Path: filepath.Join(b, "fresh.txt"), Type: watcher.EntryFile, Op: watcher.Added})
	s.pollOnce(t.Context(), b, filepath.Join(b, ".filees", "state", "head.rev"))
	if s.stagingLen() != 0 {
		t.Fatal("download remains pending at HEAD")
	}
	if err := os.WriteFile(filepath.Join(b, "fresh.txt"), []byte("real local change"), 0600); err != nil {
		t.Fatal(err)
	}
	s.acceptEvent(watcher.Event{Rel: "fresh.txt", Path: filepath.Join(b, "fresh.txt"), Type: watcher.EntryFile, Op: watcher.Modified})
	s.pollOnce(t.Context(), b, filepath.Join(b, ".filees", "state", "head.rev"))
	if s.stagingLen() != 1 {
		t.Fatal("real edit discarded")
	}
}

func TestCleanPendingConcurrentCacheWritesRemainEmpty(t *testing.T) {
	wc := t.TempDir()
	path := filepath.Join(wc, "download.txt")
	if err := os.WriteFile(path, []byte("remote"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Service{wc: wc, Cli: &revisionClient{status: []client.StatusEntry{{Path: "download.txt", Item: "normal", Props: "none"}}}, staging: make(map[string]*stageItem), cachePath: filepath.Join(wc, "cache.json")}
	s.acceptEvent(watcher.Event{Path: path, Rel: "download.txt", Type: watcher.EntryFile, Op: watcher.Added})
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.saveCacheChecked() }()
	}
	s.wcOpMu.Lock()
	s.reconcileCleanPending(t.Context(), wc)
	s.wcOpMu.Unlock()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(s.cachePath)
	if err != nil || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("stale durable cache %s: %v", b, err)
	}
}
