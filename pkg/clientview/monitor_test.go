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
