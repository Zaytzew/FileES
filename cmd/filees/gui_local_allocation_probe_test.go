//go:build windows && native_cfapi_probe && !nocfapi

package main

import (
	"context"
	"filees/internal/gui/localallocation"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Reuse the isolated CFAPI fixture: no production registration or documents.
func TestGUILocalAllocationDoesNotHydrate(t *testing.T) {
	root := t.TempDir()
	provider := newLocalStateProbe(t, root, "", "f\t33554432\tremote.bin\tremote.bin\n")
	body := make([]byte, 256<<10)
	if err := os.WriteFile(filepath.Join(root, "local.bin"), body, 0600); err != nil {
		t.Fatal(err)
	}
	manager := localallocation.New(filepath.Join(t.TempDir(), "allocation.json"))
	manager.Update([]localallocation.Root{{Server: "s", Repo: "r", Path: root}}, false)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	updates := make(chan struct{}, 1)
	go func() {
		defer close(done)
		manager.Run(ctx, func() {
			select {
			case updates <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-updates:
		result := manager.Values()["s"]
		if !result.Known || result.Bytes < 256<<10 || result.Bytes >= 32<<20 {
			t.Fatalf("allocation=%+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("measurement timed out")
	}
	select {
	case request := <-provider.fetch:
		t.Fatalf("allocation hydrated a file: %v", request)
	default:
	}
	if info, err := os.Stat(filepath.Join(root, "remote.bin")); err != nil || info.Size() != 32<<20 {
		t.Fatalf("placeholder altered: %v %v", info, err)
	}
}
