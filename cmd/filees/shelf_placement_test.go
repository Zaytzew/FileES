package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/localrepo"
)

func TestShelfPlacementNoClobberAndCrashReceipt(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.txt")
	data := []byte("accepted shelf bytes")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	fetch := localrepo.ShelfFetch{ID: "operation", Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Placement: localrepo.ShelfPlacement{ParentRepoID: "parent", ParentRoot: root, RelativePath: "result.txt"}}
	if err := placeShelfFile(source, fetch); err != nil {
		t.Fatal(err)
	}
	// Replay after atomic publication, before receipt persistence.
	if err := placeShelfFile(source, fetch); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "result.txt"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("copy %q: %v", got, err)
	}
	fetch.ID = "other-operation"
	if err := placeShelfFile(source, fetch); err == nil {
		t.Fatal("unrelated existing file accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	fetch.ID = "operation"
	if err := placeShelfFile(source, fetch); err == nil {
		t.Fatal("changed output accepted on replay")
	}
	got, _ = os.ReadFile(source)
	if string(got) != string(data) {
		t.Fatal("source modified")
	}
}

func TestShelfPlacementRejectsEscapeAndBadHash(t *testing.T) {
	for _, relative := range []string{"../outside.txt", ".svn/entries", ".filees/file", "good.txt"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(t.TempDir(), "source")
			if err := os.WriteFile(source, []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
			fetch := localrepo.ShelfFetch{ID: "test", SHA256: "bad", Size: 7, Placement: localrepo.ShelfPlacement{ParentRoot: root, RelativePath: relative}}
			if err := placeShelfFile(source, fetch); err == nil {
				t.Fatal("unsafe placement accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "good.txt")); !os.IsNotExist(err) {
				t.Fatal("unverified file published")
			}
		})
	}
}

func TestShelfDestinationRejectsNestedWCAndPortableCollision(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", ".svn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateShelfDestination(root, "nested/file.txt"); err == nil {
		t.Fatal("nested WC accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "Drawing.dwg"), []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateShelfDestination(root, "drawing.dwg"); err == nil {
		t.Fatal("portable collision accepted")
	}
	if err := validateShelfDestination(root, "CON.txt"); err == nil {
		t.Fatal("reserved device accepted")
	}
}
