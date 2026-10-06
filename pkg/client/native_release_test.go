package client

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRetainedResumeRequiresExactIdleReceipt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only recovery")
	}
	wc, retained := t.TempDir(), filepath.Join(t.TempDir(), "data.bin")
	b := RetainedPathBinding{SourceIndex: 1, RepositoryUUID: "11111111-1111-1111-1111-111111111111", URL: "file:///fixture/a.txt", Revision: 1}
	missing := New(Options{SvnPath: filepath.Join(t.TempDir(), "must-not-run")}).(*execClient)
	if err := missing.ResumeRetainedPath(t.Context(), wc, "a.txt", retained, b); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal("fallback", err)
	}
	t.Setenv("FILEES_TEST_RA_BOUND_RELEASE", "1")
	old := raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
	if err := old.ResumeRetainedPath(t.Context(), wc, "a.txt", retained, b); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal("old recovery accepted", err)
	}
	t.Setenv("FILEES_TEST_RA_BOUND_RESUME", "1")
	for _, name := range []string{"exact", "busy", "missing-idle", "uuid", "url", "revision", "normal"} {
		t.Run(name, func(t *testing.T) {
			row := map[string]any{"schema": nativeSchema, "ok": true, "state": "excluded", "wc_idle": true, "wc": filepath.ToSlash(wc), "path": "a.txt", "repos_uuid": b.RepositoryUUID, "repos_root_url": "file:///fixture", "url": b.URL, "revision": b.Revision}
			switch name {
			case "busy":
				row["wc_idle"] = false
			case "missing-idle":
				delete(row, "wc_idle")
			case "uuid":
				row["repos_uuid"] = "other"
			case "url":
				row["url"] = "file:///other/a.txt"
			case "revision":
				row["revision"] = 2
			case "normal":
				row["state"] = "normal"
			}
			raw, _ := json.Marshal(row)
			c := raFake(t, string(raw))
			err := c.ResumeRetainedPath(t.Context(), wc, "a.txt", retained, b)
			if (err == nil) != (name == "exact") {
				t.Fatalf("receipt %s: %v", name, err)
			}
		})
	}
}

func TestRetainedExclusionHasNoFallbackAndChecksBinding(t *testing.T) {
	c := New(Options{SvnPath: filepath.Join(t.TempDir(), "must-not-run")}).(*execClient)
	wc, retained := t.TempDir(), filepath.Join(t.TempDir(), "data.bin")
	b := RetainedPathBinding{SourceVolume: 1, SourceIndex: 2, RepositoryUUID: "11111111-1111-1111-1111-111111111111", URL: "file:///fixture/a.txt", Revision: 1}
	if err := c.ExcludeRetainedPath(t.Context(), wc, "a.txt", retained, b); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("without native: %v", err)
	}
	if runtime.GOOS != "windows" {
		return
	}
	c = raFake(t, `{"schema":"filees.native-svn/v1","ok":true}`)
	if err := c.ExcludeRetainedPath(t.Context(), wc, "a.txt", retained, b); !errors.Is(err, errors.ErrUnsupported) || !strings.Contains(err.Error(), "sparse_exclude_bound_v1") {
		t.Fatalf("old helper: %v", err)
	}
	for _, name := range []string{"zero-id", "zero-revision", "overflow", "uuid", "url", "query", "fragment", "relative-copy", "private-path", "parent-path"} {
		t.Run(name, func(t *testing.T) {
			bad, copyPath, rel := b, retained, "a.txt"
			switch name {
			case "zero-id":
				bad.SourceIndex = 0
			case "zero-revision":
				bad.Revision = 0
			case "overflow":
				bad.Revision = math.MaxInt32 + 1
			case "uuid":
				bad.RepositoryUUID = "not-uuid"
			case "url":
				bad.URL = "relative"
			case "query":
				bad.URL += "?other"
			case "fragment":
				bad.URL += "#other"
			case "relative-copy":
				copyPath = "data.bin"
			case "private-path":
				rel = ".svn/wc.db"
			case "parent-path":
				rel = "../a.txt"
			}
			if err := c.ExcludeRetainedPath(t.Context(), wc, rel, copyPath, bad); err == nil || !strings.Contains(err.Error(), "invalid retained exclusion binding") {
				t.Fatalf("invalid evidence: %v", err)
			}
		})
	}
}

func TestRetainedExclusionRequiresExactReceipt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("bound exclusion is Windows-only")
	}
	t.Setenv("FILEES_TEST_RA_BOUND_RELEASE", "1")
	for _, reply := range []string{
		`"state":"excluded","path":"a.txt","revision":1`,
		`"state":"other","path":"a.txt","revision":1`,
		`"state":"excluded","path":"other","revision":1`,
		`"state":"excluded","path":"a.txt","revision":2`,
		`"state":"excluded","path":"a.txt","revision":"1"`,
	} {
		t.Run(reply, func(t *testing.T) {
			c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"features":["sparse_exclude_bound_v1"],`+reply+`}`)
			b := RetainedPathBinding{SourceIndex: 1, RepositoryUUID: "11111111-1111-1111-1111-111111111111", URL: "file:///fixture/a.txt", Revision: 1}
			err := c.ExcludeRetainedPath(t.Context(), t.TempDir(), "a.txt", filepath.Join(t.TempDir(), "data.bin"), b)
			if (err == nil) != (reply == `"state":"excluded","path":"a.txt","revision":1`) {
				t.Fatalf("receipt: %v", err)
			}
		})
	}
}
