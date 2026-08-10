package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkingCopyIdentityMigratesAndRejectsAnotherAttachment(t *testing.T) {
	root := t.TempDir()
	expected := expectedWorkingCopyIdentity("manual", "repo-a", "svn+ssh://example/repo-a")
	if err := ensureWorkingCopyIdentity(root, expected); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkingCopyIdentity(root, expected); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkingCopyIdentity(root, expectedWorkingCopyIdentity("manual", "repo-b", "svn+ssh://example/repo-b")); err == nil {
		t.Fatal("working-copy identity was accepted for another attachment")
	}
	if info, err := os.Stat(filepath.Join(root, ".filees", "state", "working-copy.json")); err != nil || info.Size() == 0 {
		t.Fatalf("working-copy identity marker: info=%v err=%v", info, err)
	}
}
