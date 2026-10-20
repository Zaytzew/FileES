//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/localrepo"
)

type anchorRangeReader struct {
	client.Client
	reads atomic.Int32
}

func (p *anchorRangeReader) Revision(context.Context, string) (int64, error) { return 99, nil }
func (p *anchorRangeReader) HistoryFetchFile(_ context.Context, _ string, revision int64, dest string) (int64, error) {
	p.reads.Add(1)
	return 7, os.WriteFile(dest, []byte("fixture"), 0600)
}

func TestAnchorRangesShareOneMaterialization(t *testing.T) {
	p := &anchorRangeReader{}
	var revoked atomic.Bool
	m := &anchorManager{newSVN: func(string) (client.Client, error) {
		if revoked.Load() {
			return nil, errors.New("profile revoked")
		}
		return p, nil
	}}
	record := localrepo.Record{ServerID: "fixture", RepoURL: "svn://fixture/repo", LocalPath: t.TempDir()}
	c := &anchorFetchCache{}
	defer c.close()
	id, _ := anchorRemoteIdentity("OPZ_Zuga.pdf", 31, 7)
	first, _, err := m.materializeCached(t.Context(), record, id, 0, -1, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(first)) })
	var wg sync.WaitGroup
	for range 188 {
		wg.Go(func() {
			got, revision, err := m.materializeCached(t.Context(), record, id, 0, -1, c)
			if err != nil || got != first || revision != 31 {
				t.Errorf("range: %s r%d %v", got, revision, err)
			}
		})
	}
	wg.Wait()
	if p.reads.Load() != 1 {
		t.Fatalf("whole downloads = %d, want 1", p.reads.Load())
	}
	if _, _, err := m.materializeCached(t.Context(), record, id, 32, 7, c); err == nil {
		t.Fatal("cache bypassed pinned revision")
	}
	revoked.Store(true)
	if _, _, err := m.materializeCached(t.Context(), record, id, 0, -1, c); err == nil {
		t.Fatal("cache bypassed profile refusal")
	}
	revoked.Store(false)
	other, _ := anchorRemoteIdentity("OPZ_Zuga.pdf", 32, 7)
	dest, _, err := m.materializeCached(t.Context(), record, other, 0, -1, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(dest)) })
	if dest == first || p.reads.Load() != 2 {
		t.Fatal("another revision reused bytes")
	}
	c.close()
	if _, _, err := m.materializeCached(t.Context(), record, id, 0, -1, c); err == nil {
		t.Fatal("dead provider reused cache")
	}
}

func TestAnchorCacheCoalescesInFlightAndCancellationIsLocal(t *testing.T) {
	c := &anchorFetchCache{}
	defer c.close()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	key := anchorFetchKey{"file", 31, 7}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	load := func() (string, error) { calls.Add(1); close(started); <-release; return path, nil }
	first := make(chan error, 1)
	go func() { _, err := c.get(t.Context(), key, load); first <- err }()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.get(ctx, key, load); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() { _, err := c.get(t.Context(), key, load); second <- err }()
	close(release)
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("download stalled")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate in-flight download")
	}
}

func TestAnchorCacheFailuresAreRetriedAndCapacityIsBounded(t *testing.T) {
	c := &anchorFetchCache{}
	defer c.close()
	key := anchorFetchKey{"file", 31, 7}
	for range 2 {
		if _, err := c.get(t.Context(), key, func() (string, error) { return "", errors.New("offline") }); err == nil {
			t.Fatal("failure swallowed")
		}
		if len(c.entries) != 0 {
			t.Fatal("cached error")
		}
	}
	for i := range anchorFetchCacheEntries {
		c.entries[anchorFetchKey{fmt.Sprint(i), 31, 7}] = &anchorFetchEntry{done: make(chan struct{})}
	}
	if _, err := c.get(t.Context(), key, func() (string, error) { t.Fatal("unbounded new fetch"); return "", nil }); err == nil {
		t.Fatal("in-flight bound ignored")
	}
}

func TestAnchorHydrationRequiresExactCompleteMetadata(t *testing.T) {
	size := int64(8192)
	good := cfapiAnswer{OK: true, Placeholder: true, OnDisk: size, Size: &size, Identity: "file"}
	if !anchorHydrationComplete(good, "file", size) {
		t.Fatal("full file refused")
	}
	for _, mutate := range []func(*cfapiAnswer){
		func(a *cfapiAnswer) { a.OnDisk = 4096 }, // tail at EOF only
		func(a *cfapiAnswer) { a.Partial = true },
		func(a *cfapiAnswer) { a.Size = nil },
		func(a *cfapiAnswer) { a.Identity = "another revision" },
		func(a *cfapiAnswer) { a.Directory = true },
		func(a *cfapiAnswer) { a.Placeholder = false },
		func(a *cfapiAnswer) { a.OK = false },
	} {
		bad := good
		mutate(&bad)
		if anchorHydrationComplete(bad, "file", size) {
			t.Fatalf("incomplete metadata accepted: %+v", bad)
		}
	}
}

func TestAnchorFinalRangeDuringEarlierMetadataCheckIsNotLost(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var checks, adopted atomic.Int32
	b := &anchorBridge{retainLocal: true, revisions: map[string]anchorFetched{"tail": {identity: "file", revision: 31}, "middle": {identity: "file", revision: 31}},
		readyToAdopt: func(context.Context, string, *int64) (bool, error) {
			if checks.Add(1) == 1 {
				close(entered)
				<-release
				return false, nil
			}
			return true, nil
		}, adopt: func(context.Context, string, int64) error { adopted.Add(1); return nil }}
	done := make(chan struct{})
	go func() { b.hydrated(t.Context(), "tail"); close(done) }()
	<-entered
	b.hydrated(t.Context(), "middle")
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("adoption stalled")
	}
	if checks.Load() != 2 || adopted.Load() != 1 || len(b.revisions) != 0 {
		t.Fatalf("checks=%d adoptions=%d receipts=%d", checks.Load(), adopted.Load(), len(b.revisions))
	}
}
