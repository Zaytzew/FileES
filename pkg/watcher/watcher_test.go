package watcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMD5BacklogHashHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	if err := os.WriteFile(path, make([]byte, 1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := md5FileBudgetedContext(ctx, path, 1024*1024+1); !errors.Is(err, context.Canceled) {
		t.Fatalf("hash cancellation error = %v", err)
	}
}

func TestScannerClosesEventsAfterWorkersStop(t *testing.T) {
	wc := t.TempDir()
	scanner, err := NewScanner(Options{
		WC: wc, StatePath: filepath.Join(wc, ".filees", "state", "manifest.json"),
		ScanPeriod: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := scanner.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scanner did not close events after cancellation")
	}
}
