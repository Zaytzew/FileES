package commit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filees/pkg/activity"
	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/watcher"
)

type stagingClient struct {
	client.Client
	statusItem     string
	statuses       map[string]string
	statusErr      error
	statusCalls    int
	removeOnStatus string
	adds, commits  int
	keepCommits    int
	propSets       int
	addPaths       []string
	commitPaths    []string
	commitBatches  [][]string
	commitCh       chan struct{}
	commitErr      error
	commitOut      string
	updatedEmpty   []string
	revision       int64
	revisionErr    error
}

func (c *stagingClient) Revision(context.Context, string) (int64, error) {
	return c.revision, c.revisionErr
}

func (c *stagingClient) UpdateDepthEmpty(_ context.Context, _ string, paths []string) (string, error) {
	c.updatedEmpty = append(c.updatedEmpty, paths...)
	return "", nil
}

func (c *stagingClient) Status(_ context.Context, _ string, paths []string) ([]client.StatusEntry, error) {
	c.statusCalls++
	if c.statusErr != nil {
		return nil, c.statusErr
	}
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

func (c *stagingClient) Add(_ context.Context, _ string, paths []string) (string, error) {
	c.adds++
	c.addPaths = append([]string(nil), paths...)
	return "", nil
}

func (c *stagingClient) Delete(_ context.Context, _ string, _ []string) (string, error) {
	return "", nil
}

func (c *stagingClient) Commit(_ context.Context, _ string, paths []string, _ string) (string, error) {
	c.commits++
	c.commitPaths = append([]string(nil), paths...)
	c.commitBatches = append(c.commitBatches, append([]string(nil), paths...))
	if c.commitCh != nil {
		c.commitCh <- struct{}{}
	}
	return c.commitOut, c.commitErr
}

type activityRecorder struct{ entries []activity.Entry }

func (r *activityRecorder) Record(entry activity.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func TestActivityStagesFollowDurableCacheAndConfirmedCommit(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "report.pdf")
	if err := os.WriteFile(abs, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &activityRecorder{}
	var emitted []string
	// commitOut deliberately uses svn's Polish-locale wording ("Zatwierdzona
	// wersja" rather than "Committed revision"): the confirmed revision must
	// come from Cli.Revision (locale-independent), never by scraping this
	// free-text message.
	cli := &stagingClient{statuses: map[string]string{"report.pdf": "unversioned"}, commitOut: "Zatwierdzona wersja 18.\n", revision: 18}
	service := &Service{Cli: cli, Rules: Rules{NewLatency: time.Nanosecond, MaxBatchFiles: 10}, Activity: recorder, Emit: func(eventType string, _ any) { emitted = append(emitted, eventType) }, repoID: "docs", staging: make(map[string]*stageItem), cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json")}
	service.acceptEvent(watcher.Event{Path: abs, Rel: "report.pdf", Type: watcher.EntryFile, Op: watcher.Added})
	if err := service.tryCommit(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	var stages []activity.Stage
	for _, entry := range recorder.entries {
		stages = append(stages, entry.Stage)
	}
	want := []activity.Stage{activity.Detected, activity.Pending, activity.Publishing, activity.Published}
	if !reflect.DeepEqual(stages, want) || recorder.entries[len(recorder.entries)-1].Revision != 18 {
		t.Fatalf("activity=%+v", recorder.entries)
	}
	if len(emitted) != 5 { // four activity invalidations plus commit.completed
		t.Fatalf("events=%v", emitted)
	}
	activityEvents := 0
	for _, eventType := range emitted {
		if eventType == contract.EvActivityChanged {
			activityEvents++
		}
	}
	if activityEvents != 4 {
		t.Fatalf("events=%v", emitted)
	}
}

func TestCommitConfirmationIgnoresLocalizedCommitOutputAndUsesRevisionQuery(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "report.pdf")
	if err := os.WriteFile(abs, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	var headRev int64
	var completed []contract.CommitCompletedPayload
	cli := &stagingClient{
		statuses: map[string]string{"report.pdf": "unversioned"},
		// Real svn on a non-English locale never contains the word
		// "revision" anywhere in its commit output — this must not be the
		// source of truth for the confirmed revision.
		commitOut: "Zatwierdzona wersja 42.\n",
		revision:  42,
	}
	service := &Service{
		Cli: cli, Rules: Rules{NewLatency: time.Nanosecond, MaxBatchFiles: 10},
		OnHeadRevision: func(rev int64) { headRev = rev },
		Emit: func(eventType string, payload any) {
			if eventType == contract.EvCommitCompleted {
				completed = append(completed, payload.(contract.CommitCompletedPayload))
			}
		},
		repoID: "docs", staging: make(map[string]*stageItem), cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json"),
	}
	service.acceptEvent(watcher.Event{Path: abs, Rel: "report.pdf", Type: watcher.EntryFile, Op: watcher.Added})
	if err := service.tryCommit(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	if headRev != 42 {
		t.Fatalf("OnHeadRevision = %d, want 42", headRev)
	}
	if len(completed) != 1 || completed[0].Revision != 42 {
		t.Fatalf("commit.completed = %+v, want revision 42", completed)
	}
	head, err := os.ReadFile(filepath.Join(wc, ".filees", "state", "head.rev"))
	if err != nil || strings.TrimSpace(string(head)) != "42" {
		t.Fatalf("head.rev = %q, %v, want \"42\"", head, err)
	}
}

func TestActivityMarksPermanentCommitFailureAsFailedWithCorrelatedErrorID(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "rejected.txt")
	if err := os.WriteFile(abs, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &activityRecorder{}
	cli := &stagingClient{statuses: map[string]string{"rejected.txt": "unversioned"}, commitErr: errors.New("svn: E165001: commit blocked by pre-commit hook")}
	service := &Service{Cli: cli, Rules: Rules{NewLatency: time.Nanosecond, MaxBatchFiles: 10}, Activity: recorder, repoID: "docs", staging: make(map[string]*stageItem), cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json")}
	service.acceptEvent(watcher.Event{Path: abs, Rel: "rejected.txt", Type: watcher.EntryFile, Op: watcher.Added})
	if err := service.tryCommit(context.Background(), wc); err == nil {
		t.Fatal("expected permanent commit error")
	}
	last := recorder.entries[len(recorder.entries)-1]
	if last.Stage != activity.Failed || last.ErrorID == "" || last.Revision != 0 {
		t.Fatalf("activity=%+v, want Failed with a non-empty ErrorID", last)
	}
}

func TestActivityAdvancesAlreadyPublishedPathToPublished(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "already-normal.txt")
	if err := os.WriteFile(abs, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &activityRecorder{}
	cli := &stagingClient{statuses: map[string]string{"already-normal.txt": "normal"}, revision: 7}
	service := &Service{
		Cli: cli, Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		Activity: recorder, repoID: "docs",
		staging: map[string]*stageItem{
			"already-normal.txt": {Rel: "already-normal.txt", Abs: abs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Second)},
		},
	}
	// This is exactly the lost-reply / initial-import / prior-instance scenario:
	// the path was published through some route other than this commit, and
	// svn status is the only proof. Without the fix, the entry recorded by a
	// hypothetical earlier acceptEvent would stay Pending forever.
	if err := service.tryCommitMode(context.Background(), wc, true); err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("activity=%+v, want exactly one Published entry", recorder.entries)
	}
	if entry := recorder.entries[0]; entry.Stage != activity.Published || entry.Revision != 7 {
		t.Fatalf("activity=%+v, want Published at revision 7", entry)
	}
}

func TestActivityAdvancesAlreadyPublishedModifiedPathToPublishedInsteadOfLoopingPending(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "style.md")
	if err := os.WriteFile(abs, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &activityRecorder{}
	// A modified path whose SVN status is already "normal": another daemon
	// instance (or an earlier commit cycle before a restart raced this one)
	// already published this exact change. Before the fix, a Modified path
	// never got a status check at all, so it was resubmitted to svn commit
	// every cycle; the resulting no-op commit (nothing to commit, no
	// revision in the output) rolled the entry back to Pending forever.
	cli := &stagingClient{statuses: map[string]string{"style.md": "normal"}, revision: 9}
	service := &Service{
		Cli: cli, Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		Activity: recorder, repoID: "docs",
		staging: map[string]*stageItem{
			"style.md": {Rel: "style.md", Abs: abs, Op: watcher.Modified, FirstSeen: time.Now().Add(-time.Second)},
		},
	}
	if err := service.tryCommitMode(context.Background(), wc, true); err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if cli.commits != 0 {
		t.Fatalf("commits=%d, want 0 (already-normal path must not be resubmitted)", cli.commits)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("activity=%+v, want exactly one Published entry", recorder.entries)
	}
	if entry := recorder.entries[0]; entry.Stage != activity.Published || entry.Revision != 9 || entry.Kind != activity.Modified {
		t.Fatalf("activity=%+v, want Modified Published at revision 9", entry)
	}
}

func (c *stagingClient) CommitKeepLocks(ctx context.Context, wc string, paths []string, message string) (string, error) {
	c.keepCommits++
	return c.Commit(ctx, wc, paths, message)
}

func (c *stagingClient) PropSet(_ context.Context, _, _, _ string, _ []string) (string, error) {
	c.propSets++
	return "", nil
}

func TestEditPassportCommitKeepsLocksAndFreezesFencing(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "edited.txt")
	if err := os.WriteFile(abs, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{statuses: map[string]string{"edited.txt": "modified"}}
	var authorized []string
	released := false
	published := false
	s := &Service{
		Cli:   cli,
		Rules: Rules{NeedsLock: true, NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		BeginPublish: func(_ context.Context, paths []string) (func(), error) {
			authorized = append([]string(nil), paths...)
			return func() { released = true }, nil
		},
		OnPathsPublished: func(paths []string) {
			published = len(paths) == 1 && paths[0] == abs
		},
		staging: map[string]*stageItem{
			"edited.txt": {Rel: "edited.txt", Abs: abs, Op: watcher.Modified},
		},
	}
	if err := s.tryCommitMode(context.Background(), wc, true); err != nil {
		t.Fatal(err)
	}
	if cli.keepCommits != 1 || cli.commits != 1 {
		t.Fatalf("keep commits=%d total commits=%d", cli.keepCommits, cli.commits)
	}
	if !reflect.DeepEqual(authorized, []string{abs}) || !released || !published {
		t.Fatalf("authorized=%#v released=%v published=%v", authorized, released, published)
	}
}

func TestEditPassportProtectsDeletedFileAndForgetsItAfterCommit(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "deleted.txt")
	cli := &stagingClient{statuses: map[string]string{"deleted.txt": "missing"}}
	var authorized, removed []string
	s := &Service{
		Cli:   cli,
		Rules: Rules{NeedsLock: true, NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		BeginPublish: func(_ context.Context, paths []string) (func(), error) {
			authorized = append([]string(nil), paths...)
			return func() {}, nil
		},
		OnPathsRemoved: func(paths []string) { removed = append([]string(nil), paths...) },
		staging: map[string]*stageItem{
			"deleted.txt": {Rel: "deleted.txt", Abs: abs, Op: watcher.Deleted},
		},
	}
	if err := s.tryCommitMode(context.Background(), wc, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(authorized, []string{abs}) || !reflect.DeepEqual(removed, []string{abs}) {
		t.Fatalf("authorized=%#v removed=%#v", authorized, removed)
	}
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
	if err := s.tryCommitMode(context.Background(), wc, true); err == nil {
		t.Fatal("first commit unexpectedly succeeded")
	}
	if cli.commits != 1 || len(s.staging) != 1 {
		t.Fatalf("after lost reply: commits=%d staging=%d, want 1/1", cli.commits, len(s.staging))
	}

	// A restarted client sees the path as already versioned/normal after the
	// server accepted the commit; it must clear the cache entry without retrying.
	cli.commitErr = nil
	cli.statuses["accepted.txt"] = "normal"
	if err := s.tryCommitMode(context.Background(), wc, true); err != nil {
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
	if err := s.tryCommitMode(context.Background(), wc, true); err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if cli.commits != 0 || len(s.staging) != 0 {
		t.Fatalf("commits=%d staging=%d, want 0/0", cli.commits, len(s.staging))
	}
	stats := s.RecoveryStats()
	if stats.CacheResumed != 1 || stats.AlreadyAccepted != 1 || stats.CommitBatches != 0 {
		t.Fatalf("recovery stats = %+v, want resumed=1 accepted=1 batches=0", stats)
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
	s.Run(context.Background(), "repo", wc, events)
	if cli.commits != 1 || len(s.staging) != 0 {
		t.Fatalf("commits=%d staging=%d", cli.commits, len(s.staging))
	}
}

func TestRunFlushesStableModificationAtBacklogWatermark(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "watermark.bin")
	if err := os.WriteFile(abs, make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{}, 1)
	cli := &stagingClient{commitCh: committed}
	events := make(chan watcher.Event, 1)
	done := make(chan struct{})
	s := &Service{Cli: cli, Rules: Rules{Window: time.Hour, NewLatency: time.Hour, MaxBatchFiles: 10, MaxBatchBytes: 1024, BacklogFlushBytes: 5, ShutdownTimeout: time.Second}}
	go func() { s.Run(context.Background(), "repo", wc, events); close(done) }()
	events <- watcher.Event{Path: abs, Rel: "watermark.bin", Type: watcher.EntryFile, Op: watcher.Modified}
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

func TestRunDoesNotFlushImmatureAddedFileAtBacklogWatermark(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "large-new.bin")
	if err := os.WriteFile(abs, make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{}, 1)
	cli := &stagingClient{statuses: map[string]string{"large-new.bin": "unversioned"}, commitCh: committed}
	events := make(chan watcher.Event, 1)
	done := make(chan struct{})
	s := &Service{Cli: cli, Rules: Rules{Window: time.Hour, NewLatency: time.Hour, MaxBatchFiles: 10, MaxBatchBytes: 1024, BacklogFlushBytes: 5, ShutdownTimeout: time.Second}}
	go func() { s.Run(context.Background(), "repo", wc, events); close(done) }()
	events <- watcher.Event{Path: abs, Rel: "large-new.bin", Type: watcher.EntryFile, Op: watcher.Added}

	select {
	case <-committed:
		t.Fatal("immature added file committed merely because it crossed the watermark")
	case <-time.After(100 * time.Millisecond):
	}
	if got := s.stagingLen(); got != 1 {
		t.Fatalf("staging=%d, want 1 immature add", got)
	}
	if cli.statusCalls != 0 {
		t.Fatalf("immature watermark caused %d svn status calls", cli.statusCalls)
	}

	// Shutdown drain is deliberately forceful and must still publish the file.
	close(events)
	select {
	case <-committed:
	case <-time.After(time.Second):
		t.Fatal("shutdown drain did not publish the stable final file")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("service did not stop after shutdown drain")
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

func TestParentPathsCloseAddedChildOverAncestors(t *testing.T) {
	got := parentPaths("root/branch/deep/file.bin")
	want := []string{"root/branch/deep", "root/branch", "root"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parentPaths() = %#v, want %#v", got, want)
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
	s.drain(context.Background(), wc)
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
	if err := s.tryCommit(context.Background(), wc); err != nil {
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
	if err := s.tryCommit(context.Background(), wc); err != nil {
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
	if err := s.tryCommit(context.Background(), t.TempDir()); err != nil {
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
	var removed []string
	s := &Service{
		Cli: cli, Rules: Rules{MaxBatchFiles: 10, MaxBatchBytes: 1024},
		OnPathsRemoved: func(paths []string) { removed = append(removed, paths...) },
		staging: map[string]*stageItem{
			"new.txt": {Rel: "new.txt", Abs: abs, OldRel: "gone.txt", Op: watcher.Renamed},
		},
	}
	if err := s.tryCommit(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	if len(cli.addPaths) != 1 || cli.addPaths[0] != "new.txt" {
		t.Fatalf("add paths = %#v", cli.addPaths)
	}
	if len(cli.commitPaths) != 1 || cli.commitPaths[0] != "new.txt" {
		t.Fatalf("commit paths = %#v", cli.commitPaths)
	}
	if len(removed) != 0 {
		t.Fatalf("degraded rename reported an uncommitted removal: %#v", removed)
	}
}

func TestTryCommitPreservesDeferredRenameWhenOtherPathsCommit(t *testing.T) {
	wc := t.TempDir()
	readyAbs := filepath.Join(wc, "ready.txt")
	renameAbs := filepath.Join(wc, "renamed.txt")
	for path, body := range map[string]string{readyAbs: "ready", renameAbs: "renamed"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// This reproduces the real chaos sequence: another add is publishable,
	// while a rename whose source was never versioned has no conclusive status
	// for its destination in this status response.
	cli := &stagingClient{statuses: map[string]string{"ready.txt": "unversioned"}}
	s := &Service{
		Cli:   cli,
		Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		staging: map[string]*stageItem{
			"ready.txt":   {Rel: "ready.txt", Abs: readyAbs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Minute)},
			"renamed.txt": {Rel: "renamed.txt", Abs: renameAbs, OldRel: "original.txt", Op: watcher.Renamed, FirstSeen: time.Now().Add(-time.Minute)},
		},
	}

	if err := s.tryCommit(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	if cli.commits != 1 || len(cli.commitPaths) != 1 || cli.commitPaths[0] != "ready.txt" {
		t.Fatalf("commit paths=%#v commits=%d, want only ready.txt", cli.commitPaths, cli.commits)
	}
	if _, ok := s.staging["renamed.txt"]; !ok {
		t.Fatal("inconclusive renamed destination was lost after another path committed")
	}
	if _, ok := s.staging["ready.txt"]; ok {
		t.Fatal("successfully committed path remains staged")
	}

	cli.statuses["renamed.txt"] = "unversioned"
	if err := s.tryCommit(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("staging after retry=%#v, want empty", s.staging)
	}
	if cli.commits != 2 || len(cli.commitPaths) != 1 || cli.commitPaths[0] != "renamed.txt" {
		t.Fatalf("retry paths=%#v commits=%d", cli.commitPaths, cli.commits)
	}
}

func TestTryCommitPreservesBatchWhenStatusFails(t *testing.T) {
	wc := t.TempDir()
	abs := filepath.Join(wc, "pending.txt")
	if err := os.WriteFile(abs, []byte("pending"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{statusErr: errors.New("transient status failure")}
	s := &Service{
		Cli:   cli,
		Rules: Rules{NewLatency: time.Millisecond, MaxBatchFiles: 10, MaxBatchBytes: 1024},
		staging: map[string]*stageItem{
			"pending.txt": {Rel: "pending.txt", Abs: abs, Op: watcher.Added, FirstSeen: time.Now().Add(-time.Minute)},
		},
	}

	if err := s.tryCommit(context.Background(), wc); err == nil {
		t.Fatal("status failure was ignored")
	}
	if _, ok := s.staging["pending.txt"]; !ok {
		t.Fatal("pending path was lost after status failure")
	}
	if cli.adds != 0 || cli.commits != 0 {
		t.Fatalf("SVN mutation after status failure: adds=%d commits=%d", cli.adds, cli.commits)
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
	if err := s.tryCommit(context.Background(), wc); err != nil {
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
	if err := s.tryCommit(context.Background(), wc); err != nil {
		t.Fatal(err)
	}
	if len(s.staging) != 0 {
		t.Fatalf("staging still contains addition removed after status: %#v", s.staging)
	}
	if cli.adds != 0 || cli.commits != 0 {
		t.Fatalf("SVN called after file disappeared: adds=%d commits=%d", cli.adds, cli.commits)
	}
}
