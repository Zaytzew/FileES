//go:build linux

package singleinstance

import (
	"errors"
	"testing"
)

func TestAcquireRejectsSecondInstanceAndReleases(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	first, err := Acquire("filees-gui-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire("filees-gui-test"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Acquire("filees-gui-test")
	if err != nil {
		t.Fatalf("Acquire() after release: %v", err)
	}
	_ = third.Close()
}
