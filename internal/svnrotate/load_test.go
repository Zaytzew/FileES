package svnrotate

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLoadConfig(repo, archive string) LoadConfig {
	return LoadConfig{RepoPath: repo, ArchiveDir: archive}
}

// TestLoadGenerationFullCycle mirrors TestRotateFullCycle's shape but for the
// LOAD_REPOSITORY_DUMP direction: a fresh carrier repository gets replaced by
// the content of an externally supplied dump, never by a dump of its own
// HEAD.
func TestLoadGenerationFullCycle(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	carrier := buildTestRepo(t, root, "carrier payload\n") // stands in for the r1-only repo created by CREATE_REPOSITORY + the client's carrier commit
	archive := filepath.Join(root, "archive")

	carrierUUID, err := repoUUID(carrier)
	if err != nil {
		t.Fatal(err)
	}

	dump := testDump("first\n", "second\n")
	meta, err := LoadGeneration(testLoadConfig(carrier, archive), bytes.NewReader(dump), "test load", io.Discard)
	if err != nil {
		t.Fatalf("LoadGeneration: %v", err)
	}

	// The hot path now serves the loaded dump's content, not the carrier's.
	tree := svnlookOut(t, "tree", "--full-paths", "-r", "2", carrier)
	if !strings.Contains(tree, "docs/a.bin") {
		t.Fatalf("new generation missing loaded content: %s", tree)
	}
	if _, err := os.Stat(filepath.Join(carrier, "conf")); err != nil {
		t.Fatalf("new generation missing conf/: %v", err)
	}

	// Fresh UUID, never the carrier's and never the dump's own (svnadmin
	// load --ignore-uuid mints a new one at create time already).
	newUUID, err := repoUUID(carrier)
	if err != nil {
		t.Fatal(err)
	}
	if newUUID == carrierUUID {
		t.Fatal("new generation kept the carrier's UUID — fake continuity")
	}
	if meta.OldUUID != carrierUUID || meta.NewUUID != newUUID {
		t.Fatalf("meta UUIDs = %+v, want old=%s new=%s", meta, carrierUUID, newUUID)
	}

	// Carrier archived, not deleted.
	if _, err := os.Stat(filepath.Join(meta.ArchiveDir, "format")); err != nil {
		t.Fatalf("archived carrier missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(meta.ArchiveDir, "FROZEN")); err != nil {
		t.Fatalf("FROZEN marker missing: %v", err)
	}
	metaRaw, err := os.ReadFile(filepath.Join(archive, meta.Tag+".meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Meta
	if err := json.Unmarshal(metaRaw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.NewUUID != newUUID {
		t.Fatalf("archived meta.json new_uuid = %s, want %s", onDisk.NewUUID, newUUID)
	}
	if _, err := os.Stat(filepath.Join(archive, meta.Tag+".log.xml")); err != nil {
		t.Fatalf("carrier history manifest missing: %v", err)
	}

	// Commits are unblocked on the new generation (block hook lives only in
	// the frozen archive).
	hookBody, err := os.ReadFile(filepath.Join(carrier, "hooks", "pre-commit"))
	if err == nil && strings.Contains(string(hookBody), "rotation in progress") {
		t.Fatal("new generation still carries the maintenance block hook")
	}
}

// TestLoadGenerationRejectsCorruptDumpWithoutTouchingRepo is the direct
// contract behind EXECUTION_ORDER.md Etap 3's exit gate: a bad dump must
// fail before the swap, leaving the active (carrier) repository untouched.
func TestLoadGenerationRejectsCorruptDumpWithoutTouchingRepo(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	carrier := buildTestRepo(t, root, "carrier payload\n")
	archive := filepath.Join(root, "archive")

	before, err := repoUUID(carrier)
	if err != nil {
		t.Fatal(err)
	}
	beforeTree := svnlookOut(t, "tree", "--full-paths", "-r", "1", carrier)

	garbage := bytes.NewReader([]byte("this is not an SVN dump stream at all\n"))
	if _, err := LoadGeneration(testLoadConfig(carrier, archive), garbage, "test corrupt", io.Discard); err == nil {
		t.Fatal("corrupt dump was accepted")
	}

	after, err := repoUUID(carrier)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("carrier UUID changed despite a rejected load")
	}
	afterTree := svnlookOut(t, "tree", "--full-paths", "-r", "1", carrier)
	if afterTree != beforeTree {
		t.Fatalf("carrier tree changed despite a rejected load:\nbefore=%s\nafter=%s", beforeTree, afterTree)
	}
	// The commit-blocking hook must not survive a failed attempt.
	hookBody, err := os.ReadFile(filepath.Join(carrier, "hooks", "pre-commit"))
	if err == nil && strings.Contains(string(hookBody), "rotation in progress") {
		t.Fatal("block hook left in place after a failed load")
	}
}

func TestLoadGenerationRejectsActiveLocksUnlessBroken(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	carrier := buildTestRepo(t, root, "carrier payload\n")
	archive := filepath.Join(root, "archive")

	wc := filepath.Join(root, "wc")
	if err := runTool(nil, io.Discard, "svn", "checkout", "-q", fileURL(carrier), wc); err != nil {
		t.Fatal(err)
	}
	if err := runTool(nil, io.Discard, "svn", "lock", "-q", "-m", "lease", filepath.Join(wc, "docs", "a.bin")); err != nil {
		t.Fatal(err)
	}

	dump := testDump("first\n")
	if _, err := LoadGeneration(testLoadConfig(carrier, archive), bytes.NewReader(dump), "test locks", io.Discard); err == nil {
		t.Fatal("load proceeded despite an active lock")
	}

	cfg := testLoadConfig(carrier, archive)
	cfg.BreakLocks = true
	if _, err := LoadGeneration(cfg, bytes.NewReader(dump), "test locks broken", io.Discard); err != nil {
		t.Fatalf("BreakLocks=true still rejected: %v", err)
	}
}
