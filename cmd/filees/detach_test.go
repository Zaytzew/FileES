package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestStripWorkingCopyMetadataLeavesOrdinaryFolderAndUserData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wc")
	operationID := uuid.NewString()
	for _, directory := range []string{
		filepath.Join(root, ".svn", "pristine"),
		filepath.Join(root, ".filees", "state"),
		filepath.Join(root, "projekty", "2026"),
		filepath.Join(root, ".filees-user"),
		filepath.Join(root, ".filees-detach-"+operationID+"-svn"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	userFiles := map[string]string{
		"raport.txt":                                                "treść użytkownika",
		filepath.Join("projekty", "2026", "a"):                      "A",
		filepath.Join(".filees-user", "keep"):                       "not metadata",
		filepath.Join(".filees-detach-"+operationID+"-svn", "keep"): "also user data",
	}
	for relative, content := range userFiles {
		if err := os.WriteFile(filepath.Join(root, relative), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := stripWorkingCopyMetadata(root, operationID); err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []string{".svn", ".filees"} {
		if _, err := os.Lstat(filepath.Join(root, metadata)); !os.IsNotExist(err) {
			t.Fatalf("%s survived detach: %v", metadata, err)
		}
	}
	for relative, want := range userFiles {
		got, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil || string(got) != want {
			t.Fatalf("user file %s=%q err=%v, want %q", relative, got, err, want)
		}
	}
	if err := stripWorkingCopyMetadata(root, operationID); err != nil {
		t.Fatalf("idempotent metadata stripping: %v", err)
	}
}

func TestStripWorkingCopyMetadataRejectsUnsafeMetadataSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wc")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(filepath.Join(root, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".svn", "keep"), []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".filees")); err != nil {
		t.Fatal(err)
	}
	if err := stripWorkingCopyMetadata(root, uuid.NewString()); err == nil {
		t.Fatal("metadata symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".svn", "keep")); err != nil {
		t.Fatalf("validation removed .svn before rejecting .filees: %v", err)
	}
}
