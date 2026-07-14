package contracttests

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/watcher"
)

type watcherManifestEntry struct {
	Path  string `json:"path"`
	Mtime int64  `json:"mtime"`
	Size  int64  `json:"size,omitempty"`
	MD5   string `json:"md5,omitempty"`
}

type watcherBacklogEntry struct {
	Rel      string `json:"rel"`
	Size     int64  `json:"size"`
	Mtime    int64  `json:"mtime"`
	QueuedAt int64  `json:"queued_at"`
}

// TestWatcherBacklogWorkerWithConcurrentScanning exercises the real five-second
// backlog worker while the scanner continuously replaces its in-memory manifest.
// Besides checking the result, running this test with -race covers the ownership
// boundary between scanCycle, backlog persistence and pendingMD5 application.
func TestWatcherBacklogWorkerWithConcurrentScanning(t *testing.T) {
	wc := t.TempDir()
	stateDir := filepath.Join(wc, ".filees", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	data := make([]byte, 2*1024*1024)
	for i := range data {
		data[i] = byte((i * 31) % 251)
	}
	filePath := filepath.Join(wc, "large.bin")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(stateDir, "manifest.json")
	writeJSONFile(t, manifestPath, []watcherManifestEntry{{
		Path:  "large.bin",
		Mtime: info.ModTime().Unix(),
		Size:  info.Size(),
	}})

	backlogPath := filepath.Join(stateDir, "md5.backlog.json")
	writeJSONFile(t, backlogPath, []watcherBacklogEntry{{
		Rel:      "large.bin",
		Mtime:    info.ModTime().Unix(),
		Size:     info.Size(),
		QueuedAt: time.Now().Unix(),
	}})

	scanner, err := watcher.NewScanner(watcher.Options{
		WC:               wc,
		StatePath:        manifestPath,
		ScanPeriod:       20 * time.Millisecond,
		DeletedDebounce:  100 * time.Millisecond,
		UseMD5:           true,
		MD5PerFileCutoff: 1,
		MD5BudgetBytes:   1,
		ChanSize:         128,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	events := scanner.Start(ctx)
	drained := make(chan struct{})
	go func() {
		for range events {
		}
		close(drained)
	}()

	deadline := time.Now().Add(8 * time.Second)
	for {
		if backlogFileIsEmpty(backlogPath) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-drained
			t.Fatal("backlog worker did not process the seeded entry")
		}
		time.Sleep(25 * time.Millisecond)
	}

	// The worker persists the empty backlog before the next scan applies pendingMD5.
	// Allow several scan periods before requesting the final manifest save.
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop after context cancellation")
	}

	var manifest []watcherManifestEntry
	readJSONFile(t, manifestPath, &manifest)
	wantSum := md5.Sum(data)
	wantMD5 := hex.EncodeToString(wantSum[:])
	for _, entry := range manifest {
		if entry.Path == "large.bin" {
			if entry.MD5 != wantMD5 {
				t.Fatalf("manifest MD5 = %q, want %q", entry.MD5, wantMD5)
			}
			return
		}
	}
	t.Fatal("large.bin missing from final manifest")
}

func TestWatcherExcludesPrivateFileESStateButKeepsTickets(t *testing.T) {
	wc := t.TempDir()
	stateDir := filepath.Join(wc, ".filees", "state")
	cachePath := filepath.Join(wc, ".filees", "commit_cache", "cache.json")
	ticketPath := filepath.Join(wc, ".filees", "tickets", "NOTICE-test.req")
	for _, path := range []string{cachePath, ticketPath, filepath.Join(wc, "user.bin")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	manifestPath := filepath.Join(stateDir, "manifest.json")
	scanner, err := watcher.NewScanner(watcher.Options{
		WC: wc, StatePath: manifestPath, ScanPeriod: 20 * time.Millisecond,
		DeletedDebounce: 100 * time.Millisecond, ChanSize: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := scanner.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	for range events {
	}

	var manifest []watcherManifestEntry
	readJSONFile(t, manifestPath, &manifest)
	seen := make(map[string]bool, len(manifest))
	for _, entry := range manifest {
		seen[entry.Path] = true
	}
	if seen[".filees/commit_cache/cache.json"] || seen[".filees/state/manifest.json"] {
		t.Fatalf("private .filees state leaked into manifest: %#v", seen)
	}
	if !seen[".filees/tickets/NOTICE-test.req"] || !seen["user.bin"] {
		t.Fatalf("expected ticket and user file in manifest: %#v", seen)
	}
}

func TestWatcherReemitsAddedWhenPathReturnsDuringDeletionDebounce(t *testing.T) {
	wc := t.TempDir()
	path := filepath.Join(wc, "returning.txt")
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(wc, ".filees", "state", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, manifestPath, []watcherManifestEntry{{Path: "returning.txt", Mtime: info.ModTime().Unix(), Size: info.Size()}})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	scanner, err := watcher.NewScanner(watcher.Options{WC: wc, StatePath: manifestPath, ScanPeriod: 20 * time.Millisecond, DeletedDebounce: 300 * time.Millisecond, ChanSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := scanner.Start(ctx)
	time.Sleep(60 * time.Millisecond) // at least one scan records the absence
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-events:
			if ev.Rel == "returning.txt" && ev.Op == watcher.Added {
				cancel()
				for range events {
				}
				return
			}
		case <-deadline:
			cancel()
			for range events {
			}
			t.Fatal("returning path was not re-emitted as Added")
		}
	}
}

func TestWatcherFinalScanEmitsChangeOnShutdown(t *testing.T) {
	wc := t.TempDir()
	manifestPath := filepath.Join(wc, ".filees", "state", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, manifestPath, []watcherManifestEntry{}) // active mode
	scanner, err := watcher.NewScanner(watcher.Options{WC: wc, StatePath: manifestPath, ScanPeriod: time.Hour, DeletedDebounce: time.Minute, ChanSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := scanner.Start(ctx)
	path := filepath.Join(wc, "last-second.txt")
	if err := os.WriteFile(path, []byte("final"), 0o644); err != nil {
		t.Fatal(err)
	}
	cancel()
	found := false
	for ev := range events {
		if ev.Rel == "last-second.txt" && ev.Op == watcher.Added {
			found = true
		}
	}
	if found {
		return
	}
	t.Fatal("final scan did not emit the last-second change")
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string, dst any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func backlogFileIsEmpty(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var entries []watcherBacklogEntry
	return json.Unmarshal(b, &entries) == nil && len(entries) == 0
}
