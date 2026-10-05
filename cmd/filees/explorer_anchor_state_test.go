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
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
)

func TestAnchorLocalStateBatchRetriesUnavailableMetadata(t *testing.T) {
	root := t.TempDir()
	for i := range 130 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, bookmark := range []string{"file-063", "file-127"} {
		for _, truncated := range []bool{false, true} {
			cursor := bookmark
			_, _, err := anchorLocalStateBatch(t.Context(), root, &cursor, func(paths []string) ([]cfapiAnswer, error) {
				if truncated {
					return make([]cfapiAnswer, len(paths)-1), nil
				}
				return nil, errors.New("temporary helper failure")
			})
			if err == nil || cursor != bookmark {
				t.Fatalf("failed metadata skipped batch: cursor=%q err=%v", cursor, err)
			}
		}
	}
}

func TestAnchorConfirmationReportsBusyWorkingCopy(t *testing.T) {
	repo := &ipcserver.RepoState{}
	want := errors.New("working copy busy")
	repo.SetLocalStatusFunc(func(context.Context, []string, func([]client.StatusEntry) error) error {
		return want
	})
	m := &anchorManager{repos: func(string, string) *ipcserver.RepoState { return repo }}
	if err := m.confirmLocalStates(t.Context(), localrepo.Record{}, &anchorBridge{}, nil, []string{"file"}); !errors.Is(err, want) {
		t.Fatalf("busy working copy: %v", err)
	}
}

func TestAnchorLocalStateTurnBoundsWorkAndStopsAtPassEnd(t *testing.T) {
	for _, endAt := range []int{0, 1, 3, 8} {
		t.Run(fmt.Sprint(endAt), func(t *testing.T) {
			cursor, calls := "", 0
			more := anchorLocalStateTurn(t.Context(), &cursor, func(ctx context.Context) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 2*time.Second {
					t.Fatal("missing bounded batch deadline")
				}
				cursor = fmt.Sprint(calls)
				if calls == endAt {
					cursor = ""
				}
				return nil
			})
			want := endAt
			if want == 0 {
				want = 8
			}
			if calls != want || more != (endAt == 0) {
				t.Fatalf("calls=%d more=%v", calls, more)
			}
		})
	}
}

func TestAnchorLocalStateTurnYieldsAfterSlowBatch(t *testing.T) {
	cursor, calls := "", 0
	started := time.Now()
	more := anchorLocalStateTurn(t.Context(), &cursor, func(ctx context.Context) error {
		calls++
		cursor = "visited"
		// A completed slow batch is not canceled by the turn's soft budget.
		select {
		case <-time.After(550 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	if calls != 1 || !more || cursor != "visited" || time.Since(started) >= 2*time.Second {
		t.Fatalf("slow batch: calls=%d more=%v cursor=%q", calls, more, cursor)
	}
}

func TestAnchorLocalStateTurnRetriesOnlyFailedBatchAndBacksOff(t *testing.T) {
	cursor, calls := "before", 0
	more := anchorLocalStateTurn(t.Context(), &cursor, func(context.Context) error {
		calls++
		cursor = fmt.Sprint(calls)
		if calls == 3 {
			return errors.New("busy or unavailable status")
		}
		return nil
	})
	if more || calls != 3 || cursor != "2" {
		t.Fatalf("failed turn: calls=%d more=%v cursor=%q", calls, more, cursor)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	anchorLocalStateTurn(ctx, &cursor, func(context.Context) error {
		t.Fatal("canceled turn ran a batch")
		return nil
	})
	if cursor != "2" {
		t.Fatal("cancellation lost progress")
	}
}

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

func TestAnchorLocalStateBatchResumesAfterDeletedBookmark(t *testing.T) {
	root := t.TempDir()
	for i := range 130 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cursor := "file-063"
	if err := os.Remove(filepath.Join(root, cursor)); err != nil {
		t.Fatal(err)
	}
	var first string
	_, _, err := anchorLocalStateBatch(t.Context(), root, &cursor, func(paths []string) ([]cfapiAnswer, error) {
		first = paths[0]
		return make([]cfapiAnswer, len(paths)), nil
	})
	if err != nil || first != "file-064" {
		t.Fatalf("deleted bookmark: first=%q, err=%v", first, err)
	}
}

func TestAnchorLocalStateBatchPreservesBookmarkOnCancellation(t *testing.T) {
	root := t.TempDir()
	cursor := "previous/file"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := anchorLocalStateBatch(ctx, root, &cursor, func([]string) ([]cfapiAnswer, error) {
		t.Fatal("canceled enumeration invoked helper")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || cursor != "previous/file" {
		t.Fatalf("canceled bookmark: cursor=%q, err=%v", cursor, err)
	}
}

func TestAnchorLocalStateBatchHandlesChangedBookmarkSubtree(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprint(replaced), func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "a"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "a.txt"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			cursor, want := "a/deleted", "a.txt"
			if replaced {
				// A bookmark formerly naming a file now names a directory.
				if err := os.Mkdir(filepath.Join(root, "a", "deleted"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "a", "deleted", "child"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				want = "a/deleted/child"
			}
			var first string
			_, _, err := anchorLocalStateBatch(t.Context(), root, &cursor, func(paths []string) ([]cfapiAnswer, error) {
				first = paths[0]
				return make([]cfapiAnswer, len(paths)), nil
			})
			if err != nil || first != want {
				t.Fatalf("changed subtree: first=%q, want=%q, err=%v", first, want, err)
			}
		})
	}
}

func TestAnchorWalkOrderMatchesDirectoryTraversal(t *testing.T) {
	paths := []string{"a/child", "a/z/deep", "a.txt", "ab/file", "b"}
	for i, a := range paths {
		for j, b := range paths {
			if got := anchorWalkBefore(a, b); got != (i < j) {
				t.Fatalf("order %q < %q = %v", a, b, got)
			}
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
