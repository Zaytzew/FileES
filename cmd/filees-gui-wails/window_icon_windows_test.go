//go:build windows

package main

import "testing"

// The PNG must actually convert to a small HICON.
//
// Isolated from the window on purpose: when the small icon failed to appear
// there were three candidates - no native handle yet, a conversion Windows
// refused, or a message sent to the wrong window - and only this one can be
// answered without a running interface.
func TestTheEmbeddedIconConvertsToASmallHICON(t *testing.T) {
	icon, err := createSmallIcon(appIcon)
	if err != nil {
		t.Fatalf("createSmallIcon: %v", err)
	}
	if icon == 0 {
		t.Fatal("conversion reported success and produced no handle")
	}
}
