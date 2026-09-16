//go:build native_svn_probe

package nativesvnprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Tree export for Wehikuł czasu: list-tree plans, fetch-tree copies. One RA
// session per step, so a project of thousands of files is not thousands of
// SSH handshakes.

func (f fixture) stdinCall(t *testing.T, input []byte, ok bool, args ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.probe, args...)
	cmd.Env = os.Environ()
	if runtime.GOOS != "windows" {
		cmd.Env = append(cmd.Env, "LC_ALL=C.UTF-8")
	}
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	var result map[string]any
	if json.Unmarshal(out, &result) != nil || result["schema"] != "filees.native-svn/v1" || result["ok"] != ok || (err == nil) != ok {
		t.Fatalf("%v: ok=%v output=%s error=%v", args, ok, out, err)
	}
	return result
}

func manifest(parts ...string) []byte {
	var b bytes.Buffer
	for _, part := range parts {
		b.WriteString(part)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func readPlan(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	out := map[string]map[string]any{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("plan line %q: %v", scanner.Text(), err)
		}
		path := entry["path"].(string)
		if _, dup := out[path]; dup {
			t.Fatalf("plan names %q twice", path)
		}
		out[path] = entry
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func absent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s exists (err=%v)", path, err)
		}
	}
}

func TestHistoryListTreeWritesThePlanToAFile(t *testing.T) {
	f := newFixture(t, "old.txt") // r1: old.txt, occupied.txt, folder/
	write(t, filepath.Join(f.wc, "folder", "inside.txt"), "kept in the past\n")
	if err := os.Mkdir(filepath.Join(f.wc, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	f.svnRun(t, "add", "folder/inside.txt", "empty")
	f.svnRun(t, "commit", "--username", "editor", "-m", "fill") // r2
	f.svnRun(t, "update")
	f.svnRun(t, "delete", "folder")
	f.svnRun(t, "commit", "--username", "editor", "-m", "reorganise") // r3

	plan := filepath.Join(f.root, "plan-r2.ndjson")
	got := f.jsonCall(t, true, "list-tree", "--url", f.repoURL, "--revision", "2", "--out", plan)
	wantBytes := len("first version\n") + len("another object\n") + len("kept in the past\n")
	if got["revision"] != float64(2) || got["files"] != float64(3) || got["dirs"] != float64(2) || got["bytes"] != float64(wantBytes) {
		t.Fatalf("receipt = %v", got)
	}
	entries := readPlan(t, plan)
	if len(entries) != 5 || entries["folder/inside.txt"]["size"] != float64(len("kept in the past\n")) ||
		entries["folder"]["kind"] != "dir" || entries["empty"]["kind"] != "dir" || entries["old.txt"]["kind"] != "file" {
		t.Fatalf("plan = %v", entries)
	}
	absent(t, plan+".part")

	later := filepath.Join(f.root, "plan-r3.ndjson")
	f.jsonCall(t, true, "list-tree", "--url", f.repoURL, "--revision", "3", "--out", later)
	if now := readPlan(t, later); len(now) != 3 || now["folder"] != nil {
		t.Fatalf("plan at r3 = %v", now)
	}

	f.jsonCall(t, false, "list-tree", "--url", f.repoURL, "--revision", "2", "--out", plan)
	for name, args := range map[string][]string{
		"file target":  {"--url", f.repoURL + "/old.txt", "--revision", "2", "--out", filepath.Join(f.root, "file.ndjson")},
		"no revision":  {"--url", f.repoURL, "--out", filepath.Join(f.root, "norev.ndjson")},
		"gone at r3":   {"--url", f.repoURL + "/folder", "--revision", "3", "--out", filepath.Join(f.root, "gone.ndjson")},
		"relative out": {"--url", f.repoURL, "--revision", "2", "--out", "relative.ndjson"},
	} {
		t.Run(name, func(t *testing.T) {
			f.jsonCall(t, false, append([]string{"list-tree"}, args...)...)
			if out := args[len(args)-1]; filepath.IsAbs(out) {
				absent(t, out, out+".part")
			}
		})
	}
}

func TestHistoryFetchTreeWritesRepositoryBytesUnderLocalNames(t *testing.T) {
	f := newFixture(t, "old.txt") // r1: occupied.txt = "another object\n"
	want := "first line\nId: $Id$\n"
	if err := os.MkdirAll(filepath.Join(f.wc, "Docs", "deep"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.wc, "Docs", "deep", "text.txt"), want)
	// A real symlink, not svn:special propset on a plain file: the latter
	// leaves the working copy's on-disk kind out of sync with what the
	// property claims, and svn commit refuses it ("unexpectedly changed
	// kind") on a platform that actually enforces the check.
	if err := os.Symlink("occupied.txt", filepath.Join(f.wc, "link")); err != nil {
		t.Fatalf("symlink fixture unavailable; acceptance incomplete: %v", err)
	}
	f.svnRun(t, "add", "Docs", "link")
	f.svnRun(t, "propset", "svn:eol-style", "native", "Docs/deep/text.txt")
	f.svnRun(t, "propset", "svn:keywords", "Id", "Docs/deep/text.txt")
	f.svnRun(t, "commit", "--username", "editor", "-m", "tree") // r2

	dest := filepath.Join(f.root, "export-stage")
	if err := os.MkdirAll(filepath.Join(dest, "docs(Docs)"), 0700); err != nil {
		t.Fatal(err)
	}
	call := func(input []byte, ok bool, extra ...string) map[string]any {
		t.Helper()
		args := append([]string{"fetch-tree", "--url", f.repoURL, "--revision", "2", "--dest", dest, "--manifest-stdin"}, extra...)
		return f.stdinCall(t, input, ok, args...)
	}
	got := call(manifest("Docs/deep/text.txt", "docs(Docs)/text.txt", "link", "link", "occupied.txt", "occupied.txt"), true)
	files := map[string]float64{}
	for _, item := range got["files"].([]any) {
		entry := item.(map[string]any)
		files[entry["path"].(string)] = entry["bytes"].(float64)
	}
	if len(files) != 2 || files["docs(Docs)/text.txt"] != float64(len(want)) || files["occupied.txt"] != float64(len("another object\n")) {
		t.Fatalf("files = %v", got["files"])
	}
	skipped := got["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["path"] != "link" || skipped[0].(map[string]any)["reason"] != "special" {
		t.Fatalf("skipped = %v", got["skipped"])
	}
	if content, err := os.ReadFile(filepath.Join(dest, "docs(Docs)", "text.txt")); err != nil || string(content) != want {
		t.Fatalf("content = %q, want repository bytes %q (%v)", content, want, err)
	}
	absent(t, filepath.Join(dest, "link"))

	// Nothing is replaced: a second run over an existing file fails and keeps it.
	if err := os.WriteFile(filepath.Join(dest, "occupied.txt"), []byte("local edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call(manifest("occupied.txt", "occupied.txt"), false)
	if content, _ := os.ReadFile(filepath.Join(dest, "occupied.txt")); string(content) != "local edit\n" {
		t.Fatalf("existing file replaced: %q", content)
	}

	for name, input := range map[string][]byte{
		"empty manifest":      nil,
		"odd manifest":        manifest("occupied.txt"),
		"unterminated":        []byte("old.txt\x00x0.txt"),
		"climbing local":      manifest("old.txt", "../x1.txt"),
		"colon in local":      manifest("old.txt", "c:x2.txt"),
		"climbing repository": manifest("../old.txt", "x3.txt"),
		"absolute repository": manifest("/old.txt", "x4.txt"),
		"local named twice":   manifest("old.txt", "x5.txt", "occupied.txt", "x5.txt"),
		"missing parent":      manifest("old.txt", "absent/x6.txt"),
		"not in revision":     manifest("never.txt", "x7.txt"),
		"directory as file":   manifest("Docs", "x8.txt"),
	} {
		t.Run(name, func(t *testing.T) { call(input, false) })
	}
	f.stdinCall(t, manifest("old.txt", "y1.txt"), false, "fetch-tree", "--url", f.repoURL, "--revision", "2", "--dest", dest)
	f.stdinCall(t, manifest("old.txt", "y2.txt"), false, "fetch-tree", "--url", f.repoURL, "--dest", dest, "--manifest-stdin")
	f.stdinCall(t, manifest("old.txt", "y3.txt"), false, "fetch-tree", "--url", f.repoURL, "--revision", "2", "--dest", "relative", "--manifest-stdin")
	f.stdinCall(t, manifest("old.txt", "y4.txt"), false, "fetch-tree", "--url", f.repoURL, "--revision", "2", "--dest", filepath.Join(f.root, "no-such-stage"), "--manifest-stdin")

	for _, name := range []string{"x0.txt", "x1.txt", "x2.txt", "x3.txt", "x4.txt", "x5.txt", "x7.txt", "x8.txt", "y1.txt", "y2.txt", "y3.txt"} {
		absent(t, filepath.Join(dest, name))
	}
	absent(t, filepath.Join(f.root, "x1.txt"), filepath.Join(f.root, "no-such-stage"))
	if err := filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".part") {
			t.Errorf("partial left behind: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
