//go:build native_svn_probe

package nativesvnprobe

import (
	"encoding/json"
	"os"
	"path/filepath"
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
