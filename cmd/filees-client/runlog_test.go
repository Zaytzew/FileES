package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyLockExcludesSecondRunAndReleases(t *testing.T) {
	wc := t.TempDir()
	if err := os.Mkdir(filepath.Join(wc, ".svn"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := acquireCopyLock(wc)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireCopyLock(wc); err == nil {
		second.Close()
		t.Fatal("second run acquired same working copy")
	}
	first.Close()
	third, err := acquireCopyLock(wc)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	third.Close()
}

func TestRunLogHasNoFileNamesOrErrorText(t *testing.T) {
	dir := t.TempDir()
	if err := appendRunLog(dir, "wc-01", "sync", "failed", false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "wc-01.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if len(record) != 5 || record["stage"] != "failed" || record["success"] != false {
		t.Fatalf("unexpected journal fields: %v", record)
	}
	if err := appendRunLog(dir, "../escape", "sync", "failed", false); err == nil {
		t.Fatal("unsafe copy ID escaped journal")
	}
}
