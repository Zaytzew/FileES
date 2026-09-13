package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/runtime"
	"filees/pkg/watcher"
)

func TestAdmissionBlocksCommitAndPollBeforeClientAccess(t *testing.T) {
	var admission runtime.Admission
	resume, err := admission.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	s := Service{Admission: &admission} // nil client must never be called while closed
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.tryCommitMode(ctx, t.TempDir(), true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("commit: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	s.pollOnce(ctx2, t.TempDir(), "unused")
	if s := admission.Snapshot(); s.Active != 0 || !s.Closed {
		t.Fatal(s)
	}
}

func TestMemoryStopDuringStartupPreservesWatcherQueueWithoutCommit(t *testing.T) {
	var admission runtime.Admission
	resume, err := admission.Quiesce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	life := &runtime.Lifetime{Admission: &admission}
	ctx, cancel := context.WithCancel(runtime.WithLifetime(context.Background(), life))
	life.BeginMemoryStop()
	cancel()
	wc := t.TempDir()
	cache := filepath.Join(wc, ".filees", "commit_cache")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	events := make(chan watcher.Event, 1)
	events <- watcher.Event{Rel: "pending.dwg", Path: filepath.Join(wc, "pending.dwg"), Op: watcher.Added}
	close(events)
	cli := &stagingClient{}
	s := Service{Admission: &admission, Cli: cli}
	s.Run(ctx, "test", wc, events)
	if life.Err() != nil {
		t.Fatal(life.Err())
	}
	if cli.commits != 0 || cli.adds != 0 {
		t.Fatal("shutdown published files")
	}
	restored := Service{wc: wc, cachePath: filepath.Join(cache, "cache.json"), staging: make(map[string]*stageItem)}
	restored.loadCache()
	if _, ok := restored.staging["pending.dwg"]; !ok {
		t.Fatal("pending watcher event lost")
	}
}

func TestAdmissionBlocksStartupBeforeCacheCreation(t *testing.T) {
	var admission runtime.Admission
	resume, err := admission.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	s := Service{Admission: &admission}
	wc := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.restoreStartup(ctx, wc) {
		t.Fatal("startup admitted while closed")
	}
	if _, err := os.Stat(filepath.Join(wc, ".filees")); !os.IsNotExist(err) {
		t.Fatalf("startup touched cache: %v", err)
	}
}
