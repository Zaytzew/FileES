package commit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/watcher"
)

type stagingClient struct {
	client.Client
	statusItem     string
	statuses       map[string]string
	removeOnStatus string
	adds, commits  int
	addPaths       []string
	commitPaths    []string
	commitBatches  [][]string
	commitCh       chan struct{}
	commitErr      error
}

func (c *stagingClient) Status(_ context.Context, _ string, paths []string, _, _ string) ([]client.StatusEntry, error) {
	if c.removeOnStatus != "" {
		_ = os.Remove(c.removeOnStatus)
		return []client.StatusEntry{{Path: filepath.Base(c.removeOnStatus), Item: c.statusItem}}, nil
	}
	var out []client.StatusEntry
	for _, path := range paths {
		if item, ok := c.statuses[path]; ok {
			out = append(out, client.StatusEntry{Path: path, Item: item})
		}
	}
	return out, nil
}

func (c *stagingClient) Add(_ context.Context, _ string, paths []string, _, _ string) (string, error) {
	c.adds++
	c.addPaths = append([]string(nil), paths...)
	return "", nil
}

func (c *stagingClient) Commit(_ context.Context, _ string, paths []string, _, _, _ string) (string, error) {
	c.commits++
	c.commitPaths = append([]string(nil), paths...)
	c.commitBatches = append(c.commitBatches, append([]string(nil), paths...))
	if c.commitCh != nil {
		c.commitCh <- struct{}{}
	}
	return "", c.commitErr
}

func TestAcceptedCommitWithLostReplyIsNotRetried(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "accepted.txt")
	if err := os.WriteFile(abs, []byte("accepted"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{
		statuses:  map[string]string{"accepted.txt": "unversioned"},
		commitErr: errors.New("connection reset after server accepted commit"),
	}
	s := &Service{
		Cli:   cli,
		Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		staging: map[string]*stageItem{
			"accepted.txt": {Rel: "accepted.txt", Abs: abs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Second)},
		},
	}
	if err := s.tryCommitMode(context.Background(), wc, "", "", true); err == nil {
		t.Fatal("first commit unexpectedly succeeded")
	}
	if cli.commits != 1 || len(s.staging) != 1 {
		t.Fatalf("after lost reply: commits=%d staging=%d, want 1/1", cli.commits, len(s.staging))
	}

	// A restarted client sees the path as already versioned/normal after the
	// server accepted the commit; it must clear the cache entry without retrying.
	cli.commitErr = nil
	cli.statuses["accepted.txt"] = "normal"
	if err := s.tryCommitMode(context.Background(), wc, "", "", true); err != nil {
		t.Fatalf("recovery attempt: %v", err)
	}
	if cli.commits != 1 || len(s.staging) != 0 {
		t.Fatalf("after recovery: commits=%d staging=%d, want 1/0", cli.commits, len(s.staging))
	}
}

func TestCacheResumeReconcilesAlreadyAcceptedAddedEntry(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "accepted-after-restart.txt")
	if err := os.WriteFile(abs, []byte("already on server"), 0o644); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(wc, ".filees", "commit_cache", "cache.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal([]cacheEntry{{
		Rel: "accepted-after-restart.txt", Abs: abs, Op: "added", FirstSeen: time.Now().Add(-time.Hour),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	cli := &stagingClient{statuses: map[string]string{"accepted-after-restart.txt": "normal"}}
	s := &Service{
		Cli: cli, Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		staging: make(map[string]*stageItem), cachePath: cachePath,
	}
	s.loadCache()
	if len(s.staging) != 1 {
		t.Fatalf("resumed staging=%d, want 1", len(s.staging))
	}
	if err := s.tryCommitMode(context.Background(), wc, "", "", true); err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if cli.commits != 0 || len(s.staging) != 0 {
		t.Fatalf("commits=%d staging=%d, want 0/0", cli.commits, len(s.staging))
	}
}

func TestRunDrainsAllEventsWhenInputCloses(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "on-close.txt")
	if err := os.WriteFile(abs, []byte("close"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{statuses: map[string]string{"on-close.txt": "unversioned"}}
	events := make(chan watcher.Event, 1)
	events <- watcher.Event{Path: abs, Rel: "on-close.txt", Type: watcher.EntryFile, Op: watcher.Added}
	close(events)
	s := &Service{Cli: cli, Rules: Rules{NewLatency: time.Hour, MaxBatchFiles: 10, MaxBatchBytes: 1024, ShutdownTimeout: time.Second}}
	s.Run(context.Background(), "repo", wc, "", "", events)
	if cli.commits != 1 || len(s.staging) != 0 {
		t.Fatalf("commits=%d staging=%d", cli.commits, len(s.staging))
	}
}

func TestRunFlushesImmediatelyAtBacklogWatermark(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "watermark.bin")
	if err := os.WriteFile(abs, make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{}, 1)
	cli := &stagingClient{statuses: map[string]string{"watermark.bin": "unversioned"}, commitCh: committed}
	events := make(chan watcher.Event, 1)
	done := make(chan struct{})
	s := &Service{Cli: cli, Rules: Rules{Window: time.Hour, NewLatency: time.Hour, MaxBatchFiles: 10, MaxBatchBytes: 1024, BacklogFlushBytes: 5, ShutdownTimeout: time.Second}}
	go func() { s.Run(context.Background(), "repo", wc, "", "", events); close(done) }()
	events <- watcher.Event{Path: abs, Rel: "watermark.bin", Type: watcher.EntryFile, Op: watcher.Added}
	select {
	case <-committed:
	case <-time.After(time.Second):
		t.Fatal("backlog watermark did not trigger an immediate commit")
	}
	close(events)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("service did not stop after event channel closed")
	}
}

func TestSelectBatchEnforcesFileAndByteLimits(t *testing.T) {
	wc := t.TempDir()
	makeItem := func(rel string, size int, isDir bool) pendingEntry {
		abs := filepath.Join(wc, rel)
		if isDir {
			if err := os.MkdirAll(abs, 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(abs, make([]byte, size), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return pendingEntry{item: &stageItem{Rel: rel, Abs: abs, IsDir: isDir, Op: watcher.Added}}
	}
	entries := []pendingEntry{makeItem("dir", 0, true), makeItem("a", 4, false), makeItem("b", 4, false), makeItem("c", 4, false)}
	got := selectBatch(entries, 3, 8)
	if len(got) != 3 {
		t.Fatalf("byte-limited batch len=%d, want directory + 2 files", len(got))
	}
	got = selectBatch(entries, 1, 100)
	if len(got) != 2 {
		t.Fatalf("file-limited batch len=%d, want directory + 1 file", len(got))
	}
}

func TestSelectBatchAllowsOneOversizedFile(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "huge.bin")
	if err := os.WriteFile(abs, make([]byte, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := []pendingEntry{{item: &stageItem{Rel: "huge.bin", Abs: abs, Op: watcher.Added}}}
	if got := selectBatch(entries, 10, 5); len(got) != 1 {
		t.Fatalf("oversized batch len=%d, want 1", len(got))
	}
}

func TestDrainCommitsAllPendingInBoundedBatches(t *testing.T) {
	wc := t.TempDir()
	cli := &stagingClient{statuses: map[string]string{}}
	staging := make(map[string]*stageItem)
	for _, rel := range []string{"a", "b", "c"} {
		abs := filepath.Join(wc, rel)
		if err := os.WriteFile(abs, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
		cli.statuses[rel] = "unversioned"
		staging[rel] = &stageItem{Rel: rel, Abs: abs, Op: watcher.Added, FirstSeen: time.Now()}
	}
	s := &Service{Cli: cli, Rules: Rules{MaxBatchFiles: 2, MaxBatchBytes: 1024}, staging: staging}
	s.drain(context.Background(), wc, "", "")
	if len(s.staging) != 0 {
		t.Fatalf("drain left staging: %#v", s.staging)
	}
	if len(cli.commitBatches) != 2 || len(cli.commitBatches[0]) != 2 || len(cli.commitBatches[1]) != 1 {
		t.Fatalf("commit batches = %#v", cli.commitBatches)
	}
}

func TestTryCommitCommitsPathsAlreadyStagedBySVN(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "ready.txt")
	if err := os.WriteFile(abs, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{statuses: map[string]string{"ready.txt": "added"}}
	s := &Service{Cli: cli, Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10}, staging: map[string]*stageItem{
		"ready.txt": {Rel: "ready.txt", Abs: abs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Minute)},
	}}
	if err := s.tryCommit(context.Background(), wc, "", ""); err != nil {
		t.Fatal(err)
	}
	if cli.adds != 0 || cli.commits != 1 {
		t.Fatalf("adds=%d commits=%d", cli.adds, cli.commits)
	}
	if len(cli.commitPaths) != 1 || cli.commitPaths[0] != "ready.txt" {
		t.Fatalf("commit paths = %#v", cli.commitPaths)
	}
}

func TestTryCommitAddsDirectoryAndFileNonRecursively(t *testing.T) {
	wc := t.TempDir()
	dir := filepath.Join(wc, "album")
	file := filepath.Join(dir, "track.flac")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	// SVN reports only the unversioned directory; descendants have no separate
	// status entry until their parent is scheduled.
	cli := &stagingClient{statuses: map[string]string{"album": "unversioned"}}
	firstSeen := time.Now().Add(-time.Minute)
	s := &Service{Cli: cli, Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10}, staging: map[string]*stageItem{
		"album":            {Rel: "album", Abs: dir, IsDir: true, Op: watcher.Added, FirstSeen: firstSeen},
		"album/track.flac": {Rel: "album/track.flac", Abs: file, Op: watcher.Added, FirstSeen: firstSeen},
	}}
	if err := s.tryCommit(context.Background(), wc, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(cli.addPaths) != 2 || cli.addPaths[0] != "album" || cli.addPaths[1] != "album/track.flac" {
		t.Fatalf("svn add paths = %#v", cli.addPaths)
	}
	if len(cli.commitPaths) != 2 {
		t.Fatalf("commit paths = %#v", cli.commitPaths)
	}
}

func TestTryCommitDropsNoopDeletedPath(t *testing.T) {
	cli := &stagingClient{statuses: map[string]string{}}
	s := &Service{Cli: cli, Rules: Rules{MaxBatchFiles: 10}, staging: map[string]*stageItem{
		"never-versioned.txt": {Rel: "never-versioned.txt", Op: watcher.Deleted},
	}}
	if err := s.tryCommit(context.Background(), t.TempDir(), "", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("noop delete remains staged: %#v", s.staging)
	}
	if cli.commits != 0 {
		t.Fatalf("commits=%d, want 0", cli.commits)
	}
}

func TestTryCommitDegradesRenameWithUnversionedSourceToAdd(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "new.txt")
	if err := os.WriteFile(abs, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{statuses: map[string]string{"new.txt": "unversioned"}}
	s := &Service{Cli: cli, Rules: Rules{MaxBatchFiles: 10, MaxBatchBytes: 1024}, staging: map[string]*stageItem{
		"new.txt": {Rel: "new.txt", Abs: abs, OldRel: "gone.txt", Op: watcher.Renamed},
	}}
	if err := s.tryCommit(context.Background(), wc, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(cli.addPaths) != 1 || cli.addPaths[0] != "new.txt" {
		t.Fatalf("add paths = %#v", cli.addPaths)
	}
	if len(cli.commitPaths) != 1 || cli.commitPaths[0] != "new.txt" {
		t.Fatalf("commit paths = %#v", cli.commitPaths)
	}
}

func TestTryCommitCancelsAddedFileRemovedDuringDebounce(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "gone.txt")
	s := &Service{
		Cli:   &stagingClient{},
		Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10},
		staging: map[string]*stageItem{
			"gone.txt": {Rel: "gone.txt", Abs: abs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Minute)},
		},
	}
	if err := s.tryCommit(context.Background(), wc, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("staging still contains cancelled addition: %#v", s.staging)
	}
	cli := s.Cli.(*stagingClient)
	if cli.adds != 0 || cli.commits != 0 {
		t.Fatalf("SVN called for cancelled addition: adds=%d commits=%d", cli.adds, cli.commits)
	}
}

func TestTryCommitRechecksAddedFileAfterStatus(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "racy.txt")
	if err := os.WriteFile(abs, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{statusItem: "unversioned", removeOnStatus: abs}
	s := &Service{
		Cli:   cli,
		Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10},
		staging: map[string]*stageItem{
			"racy.txt": {Rel: "racy.txt", Abs: abs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Minute)},
		},
	}
	if err := s.tryCommit(context.Background(), wc, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("staging still contains addition removed after status: %#v", s.staging)
	}
	if cli.adds != 0 || cli.commits != 0 {
		t.Fatalf("SVN called after file disappeared: adds=%d commits=%d", cli.adds, cli.commits)
	}
}
