//go:build !nocfapi

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
)

func TestAnchorLocalStateBatchBoundsFailedInspectionsAndResumes(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", ".svn", ".filees"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 130 {
		name := filepath.Join(root, fmt.Sprintf("file-%03d", i))
		if i == 0 {
			name = filepath.Join(root, "a", "nested")
		}
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{".svn", ".filees"} {
		if err := os.WriteFile(filepath.Join(root, dir, "private"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	cursor := ""
	for _, want := range []int{64, 64, 2} {
		calls := 0
		attempts := 0
		_, paths, err := anchorLocalStateBatch(t.Context(), root, &cursor, func(paths []string) ([]cfapiAnswer, error) {
			calls++
			attempts += len(paths)
			for _, rel := range paths {
				seen[rel]++
			}
			return make([]cfapiAnswer, len(paths)), nil
		})
		if err != nil || len(paths) != 0 || calls != 1 || attempts != want {
			t.Fatalf("batch: %d calls, %d attempts, %d candidates, %v", calls, attempts, len(paths), err)
		}
	}
	if cursor != "" || len(seen) != 130 {
		t.Fatalf("cursor=%q visited=%d", cursor, len(seen))
	}
	for rel, count := range seen {
		if count != 1 {
			t.Fatalf("path %q visited %d times", rel, count)
		}
	}
}

func TestAnchorSVNLocalStateRequiresPositiveStatus(t *testing.T) {
	for _, tc := range []struct {
		name         string
		row          client.StatusEntry
		clean, local bool
	}{
		{"clean", client.StatusEntry{Item: "normal", Props: "none"}, true, true},
		{"normal properties", client.StatusEntry{Item: "normal", Props: "normal"}, true, true},
		{"changed bytes", client.StatusEntry{Item: "modified", Props: "none"}, false, true},
		{"changed properties", client.StatusEntry{Item: "normal", Props: "modified"}, false, true},
		{"unknown properties", client.StatusEntry{Item: "normal"}, false, true},
		{"conflict flag", client.StatusEntry{Item: "normal", Props: "none", Conflicted: true}, false, true},
		{"conflict", client.StatusEntry{Item: "conflicted"}, false, true},
		{"replacement", client.StatusEntry{Item: "replaced"}, false, true},
		{"new", client.StatusEntry{Item: "unversioned"}, false, false},
		{"missing", client.StatusEntry{Item: "missing"}, false, false},
		{"no status", client.StatusEntry{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clean, local := anchorSVNLocalState(tc.row)
			if clean != tc.clean || local != tc.local {
				t.Fatalf("state %v %v, want %v %v", clean, local, tc.clean, tc.local)
			}
		})
	}
}

func TestAnchorLocalStateBatchRefusesTruncatedAnswer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	_, paths, err := anchorLocalStateBatch(t.Context(), root, &cursor, func([]string) ([]cfapiAnswer, error) { return nil, nil })
	if err == nil || len(paths) != 0 {
		t.Fatalf("truncated batch: %v %v", paths, err)
	}
}

func TestAnchorCanceledStateWriterIsReapedBeforeReturning(t *testing.T) {
	bridge := &anchorBridge{}
	helper, _ := runAnchorBridge(t, bridge)
	if _, err := io.WriteString(helper.toDaemon, "{\"ok\":true}\n"); err != nil {
		t.Fatal(err)
	}
	// Handshake completes before installing a simulated process-reap hook.
	ctx, cancel := context.WithCancel(t.Context())
	started, reaped := make(chan struct{}), make(chan struct{})
	bridge.stopStateWriter = func() { close(started); <-reaped }
	done := make(chan error, 1)
	go func() { done <- bridge.setLocalState(ctx, "file", "123", true) }()
	helper.answer(t)
	cancel()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("state writer not stopped")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before process exit: %v", err)
	default:
	}
	close(reaped)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAnchorLocalStateReplyAndCancellationRetireWaiters(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		bridge := &anchorBridge{}
		helper, _ := runAnchorBridge(t, bridge)
		// State requests normally start from serve's observer after setup.
		// Let the fake helper's first line reach the initialized reader too.
		if _, err := io.WriteString(helper.toDaemon, "{\"ok\":true}\n"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- bridge.setLocalState(ctx, "drawing.dwg", "123", true) }()
		fields := helper.answer(t)
		if len(fields) != 5 || fields[0] != "state-set" || fields[2] != "drawing.dwg" || fields[4] != "1" {
			t.Fatalf("request: %q", fields)
		}
		if canceled {
			cancel()
		} else {
			_, _ = io.WriteString(helper.toDaemon, "state-set\t"+fields[1]+"\t"+hresultOK+"\n")
		}
		select {
		case err := <-done:
			if canceled && !errors.Is(err, context.Canceled) || !canceled && err != nil {
				t.Fatalf("reply: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("waiter did not finish")
		}
		cancel()
		bridge.stateMu.Lock()
		remaining := len(bridge.stateWait)
		bridge.stateMu.Unlock()
		if remaining != 0 {
			t.Fatalf("retained %d waiters", remaining)
		}
		// A late/duplicate acknowledgement is ignored without blocking stdout.
		_, _ = io.WriteString(helper.toDaemon, "state-set\t"+fields[1]+"\t"+hresultOK+"\n")
	}
}
