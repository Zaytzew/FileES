package client

import (
	"errors"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
