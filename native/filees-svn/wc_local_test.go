//go:build native_svn_probe

package nativesvnprobe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (f fixture) jsonCall(t *testing.T, ok bool, args ...string) map[string]any {
	t.Helper()
	out, err := execute(t, f.wc, f.probe, args...)
	var raw map[string]any
	if jsonErr := json.Unmarshal(out, &raw); jsonErr != nil {
		t.Fatalf("invalid JSON: %v\n%s", jsonErr, out)
	}
	if raw["schema"] != "filees.native-svn/v1" {
		t.Fatalf("schema: %s", out)
	}
	if raw["ok"] != ok || (err == nil) != ok {
		t.Fatalf("expected ok=%v err=%v output=%s", ok, err, out)
	}
	return raw
}

func TestWCLocalAddStatusPropRevertDelete(t *testing.T) {
	f := newFixture(t, "old.txt")
	f.jsonCall(t, true, "verbs")
	fresh := "fresh.txt"
	write(t, filepath.Join(f.wc, fresh), "new file\n")
	f.jsonCall(t, true, "add", "--disposable-wc", f.wc, "--", fresh)
	st := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--", fresh)
	entries, _ := st["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("status entries: %#v", st["entries"])
	}
	row := entries[0].(map[string]any)
	if row["path"] != fresh || row["item"] != "added" {
		t.Fatalf("added status: %#v", row)
	}

	f.jsonCall(t, true, "propset", "--disposable-wc", f.wc, "svn:needs-lock", "*", "--", fresh)
	got := f.jsonCall(t, true, "propget", "--disposable-wc", f.wc, "svn:needs-lock", "--", fresh)
	targets, _ := got["targets"].([]any)
	if len(targets) != 1 || targets[0].(map[string]any)["value"] != "*" {
		t.Fatalf("propget: %#v", got["targets"])
	}
	f.jsonCall(t, true, "propdel", "--disposable-wc", f.wc, "svn:needs-lock", "--", fresh)
	gone := f.jsonCall(t, true, "propget", "--disposable-wc", f.wc, "svn:needs-lock", "--", fresh)
	if targets, _ := gone["targets"].([]any); len(targets) != 0 {
		t.Fatalf("propdel left value: %#v", gone["targets"])
	}

	f.jsonCall(t, true, "revert", "--disposable-wc", f.wc, "--", fresh)
	if _, err := os.Stat(filepath.Join(f.wc, fresh)); err != nil {
		t.Fatalf("revert removed bytes: %v", err)
	}
	after := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--", fresh)
	entries, _ = after["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["item"] != "unversioned" {
		t.Fatalf("reverted status: %#v", after["entries"])
	}

	f.jsonCall(t, true, "delete", "--disposable-wc", f.wc, "--", "occupied.txt")
	del := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--", "occupied.txt")
	entries, _ = del["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["item"] != "deleted" {
		t.Fatalf("delete status: %#v", del["entries"])
	}
	f.jsonCall(t, true, "cleanup", "--disposable-wc", f.wc)
	f.jsonCall(t, false, "merge", "--disposable-wc", f.wc)
}

func TestWCLocalPropgetErrorIsSingleJSONDocument(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "ghost.txt"), "unversioned\n")
	out, err := execute(t, f.wc, f.probe, "propget", "--disposable-wc", f.wc, "svn:needs-lock", "--", "ghost.txt")
	if err == nil {
		t.Fatalf("unversioned propget succeeded: %s", out)
	}
	var raw map[string]any
	if json.Unmarshal(out, &raw) != nil || raw["schema"] != "filees.native-svn/v1" || raw["ok"] != false {
		t.Fatalf("expected one error document: %s", out)
	}
	if bytes := out; countJSONObjects(string(bytes)) != 1 {
		t.Fatalf("concatenated JSON: %s", out)
	}
}

func TestWCLocalStatusKeepsItemNormalWhenOnlyPropertiesChange(t *testing.T) {
	f := newFixture(t, "old.txt")
	f.jsonCall(t, true, "propset", "--disposable-wc", f.wc, "svn:needs-lock", "*", "--", "occupied.txt")
	st := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--", "occupied.txt")
	entries, _ := st["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("status: %#v", st["entries"])
	}
	row := entries[0].(map[string]any)
	if row["item"] != "normal" || row["props"] != "modified" {
		t.Fatalf("prop-only status: %#v", row)
	}
}

func countJSONObjects(s string) int {
	n, depth := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			if depth == 0 {
				n++
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return n
}

// info is the last WC-local verb from the desktop inventory. The fields
// asserted here are the ones the daemon actually reads: VerifyCommittedMove
// needs url, repository root and the last-changed revision, and Revision()
// needs one number.
func TestWCLocalInfoAnswersAboutTheWorkingCopy(t *testing.T) {
	f := newFixture(t, "old.txt")

	root := f.jsonCall(t, true, "info", "--disposable-wc", f.wc)
	entries, _ := root["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("root info entries: %#v", root["entries"])
	}
	row := entries[0].(map[string]any)
	if row["kind"] != "dir" {
		t.Fatalf("root kind: %#v", row)
	}
	if row["url"] == nil || row["repos_root_url"] == nil || row["repos_uuid"] == nil {
		t.Fatalf("root info must identify the repository: %#v", row)
	}
	if _, ok := row["revision"].(float64); !ok {
		t.Fatalf("root revision must be a number: %#v", row["revision"])
	}

	one := f.jsonCall(t, true, "info", "--disposable-wc", f.wc, "--", "occupied.txt")
	entries, _ = one["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("file info entries: %#v", one["entries"])
	}
	row = entries[0].(map[string]any)
	if row["path"] != "occupied.txt" || row["kind"] != "file" {
		t.Fatalf("file info: %#v", row)
	}
	url, _ := row["url"].(string)
	if !strings.HasSuffix(url, "/occupied.txt") {
		t.Fatalf("file url: %#v", row["url"])
	}
	changed, ok := row["last_changed_rev"].(float64)
	if !ok || changed < 1 {
		t.Fatalf("last_changed_rev must be the commit that made it: %#v", row["last_changed_rev"])
	}
}

// info must not touch the repository. Proven by taking the repository away:
// a verb that answers with it renamed is a verb that never asked.
//
// Worth the test because the mistake is easy and silent. Passing WORKING or
// BASE as the peg revision - the obvious translation of the CLI's dst@BASE -
// sends svn_client_info4 down its RA branch (libsvn_client/info.c:353); the
// verb keeps working against a reachable server and only fails offline, which
// is the worst moment to discover it.
func TestWCLocalInfoDoesNotTouchTheRepository(t *testing.T) {
	f := newFixture(t, "old.txt")
	hidden := f.root + string(filepath.Separator) + "repo-moved-away"
	if err := os.Rename(filepath.Join(f.root, "repo"), hidden); err != nil {
		t.Fatalf("hide repository: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(hidden, filepath.Join(f.root, "repo")) })

	got := f.jsonCall(t, true, "info", "--disposable-wc", f.wc, "--", "occupied.txt")
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["path"] != "occupied.txt" {
		t.Fatalf("info without a repository: %#v", got["entries"])
	}
}
