package clientview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMonitorEmitsOnlyNewGenerationsAndStops(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "wc")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wc, "view.json")
	write := func(view View) {
		raw, _ := json.Marshal(view)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := fixture()
	write(first)
	ctx, cancel := context.WithCancel(context.Background())
	views := Monitor(ctx, &fakeUpdater{}, MonitorConfig{Sync: SyncConfig{WorkingCopy: wc, RelativeViewPath: "view.json", CachePath: filepath.Join(root, "cache", "view.json")}, Interval: 10 * time.Millisecond})
	select {
	case got := <-views:
		if got.Generation != 1 {
			t.Fatalf("generation=%d", got.Generation)
		}
	case <-time.After(time.Second):
		t.Fatal("initial generation not emitted")
	}
	select {
	case got := <-views:
		t.Fatalf("unchanged generation emitted: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	second := first
	second.Generation = 2
	second.GeneratedAt = second.GeneratedAt.Add(time.Minute)
	write(second)
	select {
	case got := <-views:
		if got.Generation != 2 {
			t.Fatalf("generation=%d", got.Generation)
		}
	case <-time.After(time.Second):
		t.Fatal("new generation not emitted")
	}
	cancel()
	select {
	case _, ok := <-views:
		if ok {
			t.Fatal("monitor emitted after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
}

// A sync that finds nothing new is still a sync, and the caller has no other
// way to learn it happened: the channel emits only on a changed generation, so
// a stable server produces the same silence as one that has stopped answering.
//
// Missing this made a healthy server render as "not yet checked" indefinitely
// in the desktop header, which is the sort of false alarm that teaches people
// to ignore the true ones.
func TestMonitorReportsSyncsThatChangedNothing(t *testing.T) {
	root := t.TempDir()
	wc := filepath.Join(root, "wc")
	if err := os.MkdirAll(wc, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(fixture())
	if err := os.WriteFile(filepath.Join(wc, "view.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	syncs := make(chan struct{}, 32)
	emitted := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	views := Monitor(ctx, &fakeUpdater{}, MonitorConfig{
		Sync:     SyncConfig{WorkingCopy: wc, RelativeViewPath: "view.json", CachePath: filepath.Join(root, "cache", "view.json")},
		Interval: 10 * time.Millisecond,
		OnSync:   func(View) { syncs <- struct{}{} },
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range views {
			emitted++
		}
	}()

	// Two reports means the unchanged poll after the first one was still
	// counted, which is the whole point.
	for i := 0; i < 2; i++ {
		select {
		case <-syncs:
		case <-time.After(3 * time.Second):
			t.Fatalf("every successful sync must be reported; got %d", i)
		}
	}
	cancel()
	<-done
	if emitted > 1 {
		t.Fatalf("an unchanged generation must not be emitted repeatedly, got %d", emitted)
	}
}
