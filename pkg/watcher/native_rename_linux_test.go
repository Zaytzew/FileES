package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeIdentityRenameSurvivesEditAndManifestReload(t *testing.T) {
	wc := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wc, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("old", "same bytes")
	write("duplicate", "same bytes")
	if fileIdentity(filepath.Join(wc, "old")) == "" {
		t.Skip("filesystem lacks stable statx birthtime identity")
	}
	manifest := filepath.Join(wc, ".filees", "state", "manifest.json")
	opts := Options{WC: wc, StatePath: manifest, UseMD5: true, RequireRenameIdentity: true}
	s, err := NewScanner(opts)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan Event, 100)
	s.scanCycle(context.Background(), out)
	// Read a fresh scanner from the baseline on disk, not the old RAM index.
	s, err = NewScanner(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(wc, "old"), filepath.Join(wc, "now")); err != nil {
		t.Fatal(err)
	}
	write("now", "edited after rename; not the old MD5")
	s.scanCycle(context.Background(), out)
	if len(out) != 1 {
		t.Fatalf("events=%d", len(out))
	}
	ev := <-out
	if ev.Op != Renamed || ev.OldRel != "old" || ev.Rel != "now" || !ev.IdentityVerified {
		t.Fatalf("wrong event: %+v", ev)
	}
}

func TestNativeModeNeverGuessesMissingIdentity(t *testing.T) {
	wc := t.TempDir()
	if err := os.WriteFile(filepath.Join(wc, "old"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewScanner(Options{WC: wc, StatePath: filepath.Join(wc, ".filees/state/manifest.json"), UseMD5: true, RequireRenameIdentity: true})
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan Event, 100)
	s.scanCycle(context.Background(), out)
	old := s.cur["old"]
	old.Identity = ""
	s.cur["old"] = old // pre-feature manifest
	if err := os.Rename(filepath.Join(wc, "old"), filepath.Join(wc, "now")); err != nil {
		t.Fatal(err)
	}
	s.scanCycle(context.Background(), out)
	if len(out) != 1 || (<-out).Op != RenameUncertain {
		t.Fatal("inconclusive rename was guessed/published as a new object")
	}
}

func TestLegacyHashRenameIsOneToOne(t *testing.T) {
	wc := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(wc, name), []byte("same"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewScanner(Options{WC: wc, StatePath: filepath.Join(wc, ".filees/state/manifest.json"), UseMD5: true})
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan Event, 100)
	s.scanCycle(context.Background(), out)
	if err := os.Rename(filepath.Join(wc, "a"), filepath.Join(wc, "new-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(wc, "b"), filepath.Join(wc, "new-b")); err != nil {
		t.Fatal(err)
	}
	s.scanCycle(context.Background(), out)
	for len(out) > 0 {
		if ev := <-out; ev.Op == Renamed {
			t.Fatalf("ambiguous hash used as identity: %+v", ev)
		}
	}
}
