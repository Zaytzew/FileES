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
