//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

type anchorRangeNativeReader struct {
	client.Client
	data             []byte
	reads, adoptions atomic.Int32
}

func (p *anchorRangeNativeReader) Revision(context.Context, string) (int64, error) { return 99, nil }
func (p *anchorRangeNativeReader) HistoryFetchFile(_ context.Context, _ string, _ int64, dest string) (int64, error) {
	p.reads.Add(1)
	return int64(len(p.data)), os.WriteFile(dest, p.data, 0600)
}
func (p *anchorRangeNativeReader) UpdateAdoptPath(context.Context, string, string, int64) (string, error) {
	p.adoptions.Add(1)
	return "", nil
}

func (p *anchorRangeNativeReader) Status(_ context.Context, _ string, paths []string) ([]client.StatusEntry, error) {
	return []client.StatusEntry{{Path: paths[0], Item: "unversioned"}}, nil
}

// Real NTFS/Cloud Files callbacks and attribute-only residency checks; remote
// fetch and SVN adoption are counted adapters, not a claim of network/SVN E2E.
func TestAnchorNativeTailThenMiddleSharesDownloadAndWaitsForWholeFile(t *testing.T) {
	helper := probeTool(t, "FILEES_CFAPI")
	root := t.TempDir()
	p := &anchorRangeNativeReader{data: bytes.Repeat([]byte("synthetic-Zuga-"), 1<<20)}
	m := &anchorManager{helper: helper, newSVN: func(string) (client.Client, error) { return p, nil }}
	if a, err := m.call(t.Context(), "", "register", "--root", root, "--identity", "synthetic-range-test"); err != nil || !a.OK {
		t.Fatalf("register: %+v %v", a, err)
	}
	t.Cleanup(func() {
		if a, err := m.call(context.Background(), "", "unregister", "--root", root); err != nil || !a.OK {
			t.Errorf("unregister: %+v %v", a, err)
		}
	})
	size := int64(len(p.data))
	id, _ := anchorRemoteIdentity("OPZ_Zuga.pdf", 31, size)
	if a, err := m.call(t.Context(), fmt.Sprintf("f\t%d\t%s\tOPZ_Zuga.pdf\n", size, id), "placeholders", "--root", root); err != nil || !a.OK {
		t.Fatalf("seed: %+v %v", a, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	streams, err := startAnchorHelper(ctx, helper, root)
	if err != nil {
		t.Fatal(err)
	}
	cache := &anchorFetchCache{}
	defer cache.close()
	record := localrepo.Record{ServerID: "fixture", RepoURL: "svn://fixture/repo", LocalPath: root}
	service := &commit.Service{Cli: p}
	service.BindWorkingCopy("fixture", root)
	connected := make(chan struct{})
	bridge := &anchorBridge{root: root, retainLocal: true, log: talk.With("range-probe"),
		onConnected: func(context.Context) { close(connected) },
		materialize: func(ctx context.Context, id string) (string, int64, error) {
			return m.materializeCached(ctx, record, id, 0, -1, cache)
		},
		readyToAdopt: func(ctx context.Context, id string, size *int64) (bool, error) {
			return m.hydrationComplete(ctx, record, id, size)
		},
		adopt: func(ctx context.Context, _ string, rev int64) error {
			return service.AdoptPath(ctx, root, "OPZ_Zuga.pdf", rev)
		},
	}
	done := make(chan error, 1)
	go func() { done <- bridge.serve(ctx, streams) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			streams.abort()
			t.Error("provider did not stop")
		}
	})
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not connect")
	}
	file, err := os.Open(filepath.Join(root, "OPZ_Zuga.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	tail := make([]byte, 4096)
	if _, err := file.ReadAt(tail, size-int64(len(tail))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tail, p.data[len(p.data)-len(tail):]) {
		t.Fatal("wrong trailer bytes")
	}
	// Let any premature legacy EOF adoption run before checking the boundary.
	time.Sleep(200 * time.Millisecond)
	state, err := m.call(t.Context(), "", "file-state", "--root", root, "--rel", "OPZ_Zuga.pdf")
	if err != nil || !state.OK || !state.Partial || state.OnDisk >= size {
		t.Fatalf("fixture did not leave a hole: %+v %v", state, err)
	}
	if p.adoptions.Load() != 0 {
		t.Fatal("EOF fragment entered SVN")
	}
	// Finish BEFORE the previously read trailer: this callback need not hit EOF.
	rest := make([]byte, len(p.data)-len(tail))
	if _, err := file.ReadAt(rest, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rest, p.data[:len(rest)]) {
		t.Fatal("wrong prefix/middle bytes")
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.adoptions.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := p.adoptions.Load(); got != 1 {
		t.Fatalf("whole-file adoptions=%d, want 1", got)
	}
	if got := p.reads.Load(); got != 1 {
		t.Fatalf("whole-file downloads=%d, want 1", got)
	}
	t.Logf("tail + middle: one %d-byte materialization, adoption only after complete residency", size)
}
