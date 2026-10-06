package client

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathExclusionRejectsIncompleteOrDifferentReceipts(t *testing.T) {
	wc := t.TempDir()
	good := func() map[string]any {
		return map[string]any{"schema": nativeSchema, "ok": true, "state": "excluded",
			"path": "folder/a.txt", "wc": filepath.ToSlash(wc), "url": "file:///fixture/folder/a.txt",
			"repos_root_url": "file:///fixture", "repos_uuid": "fixture-uuid", "revision": float64(3)}
	}
	for _, state := range []string{"excluded", "not_excluded"} {
		raw := good()
		raw["state"] = state
		got, err := parsePathExclusion(raw, wc, "folder/a.txt")
		if err != nil || got.Excluded != (state == "excluded") || got.Revision != 3 || got.RepositoryUUID != "fixture-uuid" {
			t.Fatalf("valid metadata: %+v %v", got, err)
		}
	}
	for field, value := range map[string]any{"schema": "other", "ok": false, "state": "absent", "path": "other.txt",
		"wc": filepath.ToSlash(filepath.Join(wc, "other")), "url": "file:///other/a.txt", "repos_root_url": "",
		"repos_uuid": "", "revision": float64(-1)} {
		t.Run(field, func(t *testing.T) {
			raw := good()
			raw[field] = value
			if _, err := parsePathExclusion(raw, wc, "folder/a.txt"); err == nil {
				t.Fatalf("invalid %s accepted", field)
			}
		})
	}
	for _, revision := range []any{nil, "3", float64(0), float64(3.5), float64(1e16)} {
		raw := good()
		raw["revision"] = revision
		if _, err := parsePathExclusion(raw, wc, "folder/a.txt"); err == nil {
			t.Fatalf("invalid revision accepted: %v", revision)
		}
	}
	for field := range good() {
		raw := good()
		delete(raw, field)
		if _, err := parsePathExclusion(raw, wc, "folder/a.txt"); err == nil {
			t.Fatalf("missing %s accepted", field)
		}
	}
}

func TestPathExclusionHasNoStatusOrCLIFallback(t *testing.T) {
	c := New(Options{SvnPath: filepath.Join(t.TempDir(), "must-not-run")}).(*execClient)
	if _, err := c.InspectPathExclusion(t.Context(), t.TempDir(), "a.txt"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("without native inspection: %v", err)
	}
	for _, path := range []string{"", ".", "../a.txt", ".svn/wc.db", "folder\\a.txt"} {
		if _, err := c.InspectPathExclusion(t.Context(), t.TempDir(), path); err == nil {
			t.Fatalf("invalid path accepted: %q", path)
		}
	}
	c = raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"state":"excluded"}`)
	if _, err := c.InspectPathExclusion(t.Context(), t.TempDir(), "a.txt"); !errors.Is(err, errors.ErrUnsupported) || !strings.Contains(err.Error(), "sparse_inspect_excluded_v1") {
		t.Fatalf("older helper was not refused before inspection: %v", err)
	}
}
