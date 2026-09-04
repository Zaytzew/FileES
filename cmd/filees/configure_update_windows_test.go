//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filees/pkg/config"
)

// The client updates the directory it is running from, and nothing else.
//
// A configurable install directory is a setting that can point somewhere the
// client is not, and the failure that produces is the worst kind: an update
// reports success, replaces files nobody runs, and survives a restart looking
// exactly like a client that refuses to update. The one directory a
// self-updating program can be certain about is its own.
func TestTheClientUpdatesTheDirectoryItRunsFrom(t *testing.T) {
	directory, err := clientInstallDirectory()
	if err != nil {
		t.Fatalf("clientInstallDirectory: %v", err)
	}
	if !filepath.IsAbs(directory) {
		t.Fatalf("directory = %q, want an absolute path", directory)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The test binary is what is running, so its directory is the answer.
	want := filepath.Dir(filepath.Clean(executable))
	if !strings.EqualFold(directory, want) {
		resolved, resolveErr := filepath.EvalSymlinks(executable)
		if resolveErr == nil {
			want = filepath.Dir(filepath.Clean(resolved))
		}
		if !strings.EqualFold(directory, want) {
			t.Fatalf("directory = %q, want %q", directory, want)
		}
	}
}

// The platform gate stays. A config that names another platform must be
// refused loudly rather than updating this one from somebody else's bundle.
func TestAMismatchedUpdatePlatformIsRefused(t *testing.T) {
	update := config.UpdateConfig{Platform: "linux-amd64", Channel: "alpha", Component: "desktop-client"}
	err := configureClientUpdate(nil, &update, "0.1.15")
	if err == nil {
		t.Fatal("a linux-amd64 update configuration was accepted by a windows client")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v; the reason must name the mismatch", err)
	}
}
