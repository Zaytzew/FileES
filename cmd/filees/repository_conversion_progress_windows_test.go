//go:build windows && !nocfapi

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConversionControlProgressDiscoveryCancelAndFreshTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var done []int64
	_, err := conversionPinTreeProgress(ctx, root, nil, func(n int64) { done = append(done, n); cancel() })
	if !errors.Is(err, context.Canceled) || len(done) != 1 || done[0] != 1 {
		t.Fatal(done, err)
	}
	// Cancellation closes all pins. Restart observes a new traversal, not a
	// persistent counter or custody proof inherited from the interrupted pass.
	done = nil
	var bytes int64
	pin, err := conversionPinTreeProgress(t.Context(), root, func(n int64) { bytes += n }, func(n int64) { done = append(done, n) })
	if err != nil {
		t.Fatal(err)
	}
	defer pin.close()
	if len(done) != 2 || done[0] != 1 || done[1] != 2 || bytes != 7 {
		t.Fatal(done, bytes)
	}
	var refreshed []int64
	_, err = pin.refresh(t.Context(), root, func(n int64) { refreshed = append(refreshed, n) })
	if err != nil || len(refreshed) != 2 || refreshed[1] != 2 {
		t.Fatal(refreshed, err)
	}
	if err := os.WriteFile(filepath.Join(root, "late"), []byte("not counted"), 0600); err != nil {
		t.Fatal(err)
	}
	refreshed = nil
	if _, err = pin.refresh(t.Context(), root, func(n int64) { refreshed = append(refreshed, n) }); err == nil || len(refreshed) != 0 {
		t.Fatal("failed namespace must not advance root check", refreshed, err)
	}
}

func TestConversionControlProgressReadFailureDoesNotCountObject(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var done []int64
	// Invalidate the remaining namespace after the first file was inspected;
	// the failed file must not be presented as checked.
	_, err := conversionPinTreeProgress(t.Context(), root, nil, func(n int64) {
		done = append(done, n)
		if n == 2 {
			if e := os.Remove(filepath.Join(root, "b")); e != nil {
				t.Fatal(e)
			}
		}
	})
	if !errors.Is(err, os.ErrNotExist) || len(done) != 2 || done[1] != 2 {
		t.Fatal(done, err)
	}
}
