package svnrotate

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The V1 prototype's tests only covered dump-stream transformation; the
// 2026-07-16 review flagged the missing piece explicitly: no complete cycle
// on a real FSFS repository. This file is that cycle: build a real repo via
// svnadmin load (with svn:needs-lock, custom conf, custom hook, an active
// lock), rotate it, and assert the FileES contract on the new generation.

func requireSVNTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"svnadmin", "svnlook", "svn"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func propBlock(pairs ...[2]string) []byte {
	var b bytes.Buffer
	for _, kv := range pairs {
		fmt.Fprintf(&b, "K %d\n%s\nV %d\n%s\n", len(kv[0]), kv[0], len(kv[1]), kv[1])
	}
	b.WriteString("PROPS-END\n")
	return b.Bytes()
}

// testDump builds a minimal valid dump: r1 adds docs/ and docs/a.bin with
// svn:needs-lock — the property whose survival is the whole point. Each
// further content produces one change revision of docs/a.bin.
func testDump(contents ...string) []byte {
	var d bytes.Buffer
	d.WriteString("SVN-fs-dump-format-version: 2\n\n")

	writeRevHeader := func(rev int, log string) {
		revProps := propBlock(
			[2]string{"svn:log", log},
			[2]string{"svn:author", "filees"},
			[2]string{"svn:date", fmt.Sprintf("2020-01-02T03:04:%02d.000000Z", rev)},
		)
		fmt.Fprintf(&d, "Revision-number: %d\nProp-content-length: %d\nContent-length: %d\n\n",
			rev, len(revProps), len(revProps))
		d.Write(revProps)
		d.WriteString("\n")
	}

	writeRevHeader(1, "initial import")

	dirProps := propBlock()
	fmt.Fprintf(&d, "Node-path: docs\nNode-kind: dir\nNode-action: add\nProp-content-length: %d\nContent-length: %d\n\n",
		len(dirProps), len(dirProps))
	d.Write(dirProps)
	d.WriteString("\n")

	fileProps := propBlock([2]string{"svn:needs-lock", "*"})
	fmt.Fprintf(&d, "Node-path: docs/a.bin\nNode-kind: file\nNode-action: add\nProp-content-length: %d\nText-content-length: %d\nContent-length: %d\n\n",
		len(fileProps), len(contents[0]), len(fileProps)+len(contents[0]))
	d.Write(fileProps)
	d.WriteString(contents[0])
	d.WriteString("\n\n")

	for i, content := range contents[1:] {
		writeRevHeader(i+2, fmt.Sprintf("change %d", i+2))
		fmt.Fprintf(&d, "Node-path: docs/a.bin\nNode-kind: file\nNode-action: change\nText-content-length: %d\nContent-length: %d\n\n",
			len(content), len(content))
		d.WriteString(content)
		d.WriteString("\n\n")
	}
	return d.Bytes()
}

func buildTestRepo(t *testing.T, root string, contents ...string) string {
	t.Helper()
	if len(contents) == 0 {
		contents = []string{"payload-bytes\n"}
	}
	repo := filepath.Join(root, "repo")
	if err := svnadminCreate(repo); err != nil {
		t.Fatal(err)
	}
	if err := runTool(bytes.NewReader(testDump(contents...)), io.Discard,
		"svnadmin", "load", "--quiet", repo); err != nil {
		t.Fatal(err)
	}
	return repo
}

func svnlookOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := outputTool("svnlook", args...)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func testConfig(repo, archive string) Config {
	return Config{
		RepoPath:      repo,
		ArchiveDir:    archive,
		SizeThreshold: DefaultSizeThreshold,
		MaxAge:        DefaultMaxAge,
	}
}

func TestRotateFullCycle(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	repo := buildTestRepo(t, root)
	archive := filepath.Join(root, "archive")

	// Operational config and a pre-existing hook that must both survive
	// into the new generation.
	confMarker := "# filees-test-conf-marker\n"
	svnserveConf := filepath.Join(repo, "conf", "svnserve.conf")
	orig, err := os.ReadFile(svnserveConf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svnserveConf, append([]byte(confMarker), orig...), 0o600); err != nil {
		t.Fatal(err)
	}
	hookBody := "#!/bin/sh\n# filees-test-original-hook\nexit 0\n"
	origHook := filepath.Join(repo, "hooks", "pre-commit")
	if err := os.WriteFile(origHook, []byte(hookBody), 0o755); err != nil {
		t.Fatal(err)
	}

	oldUUID, err := repoUUID(repo)
	if err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	if err := Rotate(testConfig(repo, archive), "test", &log); err != nil {
		t.Fatalf("Rotate: %v\nlog:\n%s", err, log.String())
	}

	// New generation: r1, new UUID, tree and needs-lock intact.
	if got := strings.TrimSpace(svnlookOut(t, "youngest", repo)); got != "1" {
		t.Fatalf("new generation HEAD = %s, want 1", got)
	}
	newUUID, err := repoUUID(repo)
	if err != nil {
		t.Fatal(err)
	}
	if newUUID == oldUUID {
		t.Fatal("new generation must have a NEW UUID — carrying the old one was the rejected V1 model")
	}
	if got := strings.TrimSpace(svnlookOut(t, "propget", "-r", "1", repo, "svn:needs-lock", "docs/a.bin")); got != "*" {
		t.Fatalf("svn:needs-lock = %q, want * (edit passport contract)", got)
	}
	if got := svnlookOut(t, "cat", "-r", "1", repo, "docs/a.bin"); got != "payload-bytes\n" {
		t.Fatalf("file content mismatch: %q", got)
	}

	// Operational configuration inherited, block hook not.
	conf, err := os.ReadFile(svnserveConf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(conf), confMarker) {
		t.Fatal("svnserve.conf customisation lost — new generation got svnadmin create defaults")
	}
	hook, err := os.ReadFile(origHook)
	if err != nil {
		t.Fatal(err)
	}
	if string(hook) != hookBody {
		t.Fatalf("original pre-commit not restored in new generation:\n%s", hook)
	}

	// Archive: frozen repo with block hook + FROZEN marker, manifest, meta.
	entries, err := filepath.Glob(filepath.Join(archive, "*.svn"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("archived repos: %v %v", entries, err)
	}
	frozenRepo := entries[0]
	frozenHook, err := os.ReadFile(filepath.Join(frozenRepo, "hooks", "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frozenHook), "rotation in progress") {
		t.Fatal("archived generation must keep the commit-blocking hook")
	}
	if _, err := os.Stat(filepath.Join(frozenRepo, "FROZEN")); err != nil {
		t.Fatal("FROZEN marker missing in archived generation")
	}
	manifests, _ := filepath.Glob(filepath.Join(archive, "*.log.xml"))
	if len(manifests) != 1 {
		t.Fatalf("manifests in archive: %v", manifests)
	}
	if data, err := os.ReadFile(manifests[0]); err != nil || !strings.Contains(string(data), "docs/a.bin") {
		t.Fatalf("manifest missing history detail: %v", err)
	}
	metas, _ := filepath.Glob(filepath.Join(archive, "*.meta.json"))
	if len(metas) != 1 {
		t.Fatalf("meta files in archive: %v", metas)
	}
	var meta Meta
	if data, err := os.ReadFile(metas[0]); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.OldUUID != oldUUID || meta.NewUUID != newUUID || meta.OldHead != 1 || meta.Reason != "test" {
		t.Fatalf("meta mismatch: %+v", meta)
	}

	// No dump by default (KeepDump=false) and no leftover work dirs.
	if dumps, _ := filepath.Glob(filepath.Join(archive, "*.dump.gz")); len(dumps) != 0 {
		t.Fatalf("unexpected dump artifacts: %v", dumps)
	}
	if works, _ := filepath.Glob(filepath.Join(archive, ".rotate-work-*")); len(works) != 0 {
		t.Fatalf("work dir left behind: %v", works)
	}
}

func TestRotateRefusesActiveLocks(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	repo := buildTestRepo(t, root)
	archive := filepath.Join(root, "archive")

	// An active lock is a live edit passport.
	if err := runTool(nil, io.Discard, "svn", "lock", "-m", "passport",
		fileURL(repo)+"/docs/a.bin", "--non-interactive", "--no-auth-cache"); err != nil {
		t.Skipf("cannot create test lock: %v", err)
	}

	err := Rotate(testConfig(repo, archive), "test", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("expected active-lock refusal, got: %v", err)
	}
	// Refusal must restore the hot repo: no block hook left behind.
	if _, err := os.Stat(filepath.Join(repo, "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Fatal("block hook left in hot repo after refused rotation")
	}
	if got := strings.TrimSpace(svnlookOut(t, "youngest", repo)); got != "1" {
		t.Fatalf("hot repo damaged by refused rotation: HEAD=%s", got)
	}

	// Explicit -break-locks proceeds.
	cfg := testConfig(repo, archive)
	cfg.BreakLocks = true
	if err := Rotate(cfg, "test", io.Discard); err != nil {
		t.Fatalf("Rotate with BreakLocks: %v", err)
	}
}

func TestRotateBoundedDump(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	// Three revisions: r1 "v1", r2 "v2", r3 "v3".
	repo := buildTestRepo(t, root, "v1\n", "v2\n", "v3\n")
	cfg := testConfig(repo, filepath.Join(root, "archive"))
	cfg.DumpDepth = 2
	if err := Rotate(cfg, "test", io.Discard); err != nil {
		t.Fatal(err)
	}

	// Artifact name self-describes the window r2:r3.
	dumps, _ := filepath.Glob(filepath.Join(cfg.ArchiveDir, "*.dump.gz"))
	if len(dumps) != 1 || !strings.HasSuffix(dumps[0], ".r2-r3.dump.gz") {
		t.Fatalf("dump artifacts: %v", dumps)
	}
	metas, _ := filepath.Glob(filepath.Join(cfg.ArchiveDir, "*.meta.json"))
	var meta Meta
	if data, err := os.ReadFile(metas[0]); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.DumpRange != "r2:r3" || meta.OldHead != 3 {
		t.Fatalf("meta: %+v", meta)
	}

	// The bounded dump must be independently restorable: load it into a
	// fresh repo and check the window content survived.
	f, err := os.Open(dumps[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err := svnadminCreate(restored); err != nil {
		t.Fatal(err)
	}
	if err := runTool(gz, io.Discard, "svnadmin", "load", "--quiet", "--ignore-uuid", restored); err != nil {
		t.Fatalf("bounded dump not restorable: %v", err)
	}
	if got := strings.TrimSpace(svnlookOut(t, "youngest", restored)); got != "2" {
		t.Fatalf("restored window HEAD = %s, want 2 (r2:r3 window)", got)
	}
	if got := svnlookOut(t, "cat", "-r", "2", restored, "docs/a.bin"); got != "v3\n" {
		t.Fatalf("restored window content = %q, want v3", got)
	}
	// needs-lock rides along because the window's first rev is complete.
	if got := strings.TrimSpace(svnlookOut(t, "propget", "-r", "1", restored, "svn:needs-lock", "docs/a.bin")); got != "*" {
		t.Fatalf("restored svn:needs-lock = %q", got)
	}
}

func TestRotateDumpDepthClampsToR1(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	repo := buildTestRepo(t, root) // single revision
	cfg := testConfig(repo, filepath.Join(root, "archive"))
	cfg.DumpDepth = 100
	if err := Rotate(cfg, "test", io.Discard); err != nil {
		t.Fatal(err)
	}
	dumps, _ := filepath.Glob(filepath.Join(cfg.ArchiveDir, "*.dump.gz"))
	if len(dumps) != 1 || !strings.HasSuffix(dumps[0], ".r1-r1.dump.gz") {
		t.Fatalf("dump artifacts: %v", dumps)
	}
}

func TestRotateEmptyRepoIsNoop(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := svnadminCreate(repo); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "archive")
	if err := Rotate(testConfig(repo, archive), "test", io.Discard); err != nil {
		t.Fatal(err)
	}
	if entries, _ := filepath.Glob(filepath.Join(archive, "*.svn")); len(entries) != 0 {
		t.Fatalf("empty repo must not be rotated: %v", entries)
	}
}

func TestShouldRotateTriggers(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	repo := buildTestRepo(t, root)
	archive := filepath.Join(root, "archive")

	// Size trigger: 1-byte threshold always fires.
	cfg := testConfig(repo, archive)
	cfg.SizeThreshold = 1
	rotate, reason, err := ShouldRotate(cfg, io.Discard)
	if err != nil || !rotate || !strings.Contains(reason, "size") {
		t.Fatalf("size trigger: rotate=%v reason=%q err=%v", rotate, reason, err)
	}

	// Age trigger: r1 svn:date is 2020-01-02 (from the dump), far past any
	// small MaxAge. This is the concept's declared semantics — svn:date of
	// the oldest revision, not a marker file's mtime.
	cfg = testConfig(repo, archive)
	cfg.MaxAge = time.Hour
	rotate, reason, err = ShouldRotate(cfg, io.Discard)
	if err != nil || !rotate || !strings.Contains(reason, "age") {
		t.Fatalf("age trigger: rotate=%v reason=%q err=%v", rotate, reason, err)
	}

	// Neither trigger: default thresholds on a tiny fresh-dated repo would
	// need a repo younger than 365 days; our r1 is 2020, so use a huge age.
	cfg = testConfig(repo, archive)
	cfg.MaxAge = 200 * 365 * 24 * time.Hour
	rotate, reason, err = ShouldRotate(cfg, io.Discard)
	if err != nil || rotate {
		t.Fatalf("no trigger expected: rotate=%v reason=%q err=%v", rotate, reason, err)
	}
}

func TestRotateLockExcludesConcurrent(t *testing.T) {
	requireSVNTools(t)
	root := t.TempDir()
	archive := filepath.Join(root, "archive")
	if err := os.MkdirAll(archive, 0o750); err != nil {
		t.Fatal(err)
	}
	release, err := acquireLock(filepath.Join(archive, ".rotate.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	repo := buildTestRepo(t, root)
	err = Rotate(testConfig(repo, archive), "test", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "another rotation") {
		t.Fatalf("expected concurrent-run refusal, got: %v", err)
	}
}
