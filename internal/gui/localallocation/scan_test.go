package localallocation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanCachesDirectoriesIncludesInternalAndIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{".svn/pristine/a", ".filees/state", "ignored.bak", "folder/file"} {
		path := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls, files, waits := 0, 0, 0
	scan := scanner{probe: func(path string) (metadata, error) {
		calls++
		v, e := probe(path)
		if e == nil && !v.directory {
			files++
		}
		return v, e
	}, wait: func(context.Context) error { waits++; return nil }}
	r := Root{Path: root, Hints: map[string]string{}}
	now := time.Now()
	first, err := scan.scan(t.Context(), r, record{}, now)
	if err != nil || !first.Value.Known || first.Value.State != "ready" || first.Value.Bytes <= 0 || files != 4 {
		t.Fatalf("first=%+v files=%d err=%v", first.Value, files, err)
	}
	if waits < calls {
		t.Fatal("unpaced metadata work")
	}
	files = 0
	calls = 0
	second, err := scan.scan(t.Context(), r, first, now.Add(time.Minute))
	if err != nil || second.Value.Bytes != first.Value.Bytes || files != 0 || calls != len(first.Directories) {
		t.Fatalf("cache reread files: %d/%d %v", files, calls, err)
	}
	r.Hints = map[string]string{"folder": "change"}
	_, err = scan.scan(t.Context(), r, second, now.Add(2*time.Minute))
	if err != nil || files != 1 {
		t.Fatalf("hint should reread one directory, files=%d err=%v", files, err)
	}
	files = 0
	_, err = scan.scan(t.Context(), r, second, now.Add(31*time.Minute))
	if err != nil || files != 4 {
		t.Fatalf("audit files=%d err=%v", files, err)
	}
}

func TestScanFailureAndCancellationAreNotZeroSuccess(t *testing.T) {
	scan := scanner{probe: func(string) (metadata, error) { return metadata{}, os.ErrPermission }, wait: func(context.Context) error { return nil }}
	got, err := scan.scan(t.Context(), Root{Path: t.TempDir()}, record{}, time.Now())
	if err != nil || got.Value.Known || got.Value.State != "partial" {
		t.Fatalf("%+v %v", got.Value, err)
	}
	scan.wait = func(context.Context) error { return context.Canceled }
	if _, err = scan.scan(t.Context(), Root{}, record{}, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCachedPathsCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	info, _ := probe(root)
	now := time.Now()
	old := record{Directories: map[string]directory{".": {Stamp: info.stamp, At: now, Children: []string{"../outside"}}}}
	calls := 0
	scan := scanner{probe: func(path string) (metadata, error) {
		calls++
		if path != root {
			t.Fatalf("escaped: %s", path)
		}
		return probe(path)
	}, wait: func(context.Context) error { return nil }}
	got, err := scan.scan(t.Context(), Root{Path: root}, old, now)
	if err != nil || calls != 1 || got.Value.State != "partial" {
		t.Fatalf("%+v %v", got.Value, err)
	}
}

func TestCacheAndBindingFence(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "allocation.json"))
	r := Root{Server: "s", Repo: "r", Path: t.TempDir()}
	value := Value{Known: true, Bytes: 123, State: "ready"}
	m.save(map[string]record{rootKey(r): {Value: value}})
	if got := m.load()[rootKey(r)].Value; got != value {
		t.Fatalf("cache %+v", got)
	}
	m.Update([]Root{r}, false)
	m.publish(rootKey(r), value, nil)
	if m.Values()["s"].Bytes != 123 {
		t.Fatal("missing sum")
	}
	r.Path = t.TempDir()
	m.Update([]Root{r}, false)
	if m.Values()["s"].Known {
		t.Fatal("stale path counted")
	}
	m.Update(nil, false)
	if len(m.Values()) != 0 {
		t.Fatal("detached root counted")
	}
}

func TestWorkerPacingAndBusyCancellation(t *testing.T) {
	m := New("")
	r := Root{Server: "s", Repo: "r", Path: t.TempDir()}
	m.Update([]Root{r}, false)
	start := time.Now()
	if err := m.wait(t.Context(), rootKey(r)); err != nil || time.Since(start) < 10*time.Millisecond {
		t.Fatal("missing IO pacing", err)
	}
	m.Update([]Root{r}, true)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := m.wait(ctx, rootKey(r)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	m.Update(nil, false)
	if err := m.wait(t.Context(), rootKey(r)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestStartupPublishesAllCachedRootsWhileBusy(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "cache.json"))
	a, b := Root{Server: "s", Repo: "a", Path: t.TempDir()}, Root{Server: "s", Repo: "b", Path: t.TempDir()}
	m.save(map[string]record{rootKey(a): {Value: Value{Known: true, Bytes: 10, State: "ready"}}, rootKey(b): {Value: Value{Known: true, Bytes: 20, State: "ready"}}})
	m.Update([]Root{a, b}, true)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	ready := make(chan struct{}, 2)
	go func() { defer close(done); m.Run(ctx, func() { ready <- struct{}{} }) }()
	for range 2 {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("cached result not published")
		}
	}
	if got := m.Values()["s"]; !got.Known || got.Bytes != 30 || got.State != "stale" {
		t.Fatalf("%+v", got)
	}
}

func TestFailureRetainsLastTotalAndOverlapIsDetected(t *testing.T) {
	r := Root{Path: t.TempDir()}
	old := record{Value: Value{Known: true, Bytes: 99, State: "ready"}}
	s := scanner{probe: func(string) (metadata, error) { return metadata{}, os.ErrNotExist }, wait: func(context.Context) error { return nil }}
	got, err := s.scan(t.Context(), r, old, time.Now())
	if err != nil || got.Value.Bytes != 99 || !got.Value.Known || got.Value.State != "partial" {
		t.Fatalf("%+v %v", got.Value, err)
	}
	if !overlaps(r.Path, filepath.Join(r.Path, "nested")) || overlaps(r.Path, r.Path+"-other") {
		t.Fatal("overlap boundary")
	}
}

func TestHardlinksAreReportedAsPartialRatherThanDoubleCounted(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	if err := os.WriteFile(a, make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
	s := scanner{probe: probe, wait: func(context.Context) error { return nil }}
	got, err := s.scan(t.Context(), Root{Path: root}, record{}, time.Now())
	if err != nil || got.Value.State != "partial" {
		t.Fatalf("%+v %v", got.Value, err)
	}
}
