package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeNativeRA() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		if os.Getenv("FILEES_TEST_RA_OLD_HELPER") == "1" {
			fmt.Print(`{"schema":"filees.native-svn/v1","ok":true,"features":[]}`)
			return
		}
		fmt.Print(`{"schema":"filees.native-svn/v1","ok":true,"features":["update_changes","commit_targets_stdin_v1","writer_lease_v1","sparse_checkout_v1","sparse_update_parents_v1","history_list_v1","history_raw_file_v1"]}`)
		return
	}
	// fetch-file writes its --out like the real helper, so a receipt can be
	// checked against a file that actually exists.
	if len(os.Args) > 1 && os.Args[1] == "fetch-file" {
		if body, ok := os.LookupEnv("FILEES_TEST_RA_FILE"); ok {
			for i := 2; i+1 < len(os.Args); i++ {
				if os.Args[i] == "--out" {
					if err := os.WriteFile(os.Args[i+1], []byte(body), 0600); err != nil {
						panic(err)
					}
				}
			}
		}
	}
	if p := os.Getenv("FILEES_TEST_RA_TRACE"); p != "" {
		f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			panic(e)
		}
		json.NewEncoder(f).Encode(os.Args[1:])
		if len(os.Args) > 1 && os.Args[1] == "commit" {
			b, _ := io.ReadAll(os.Stdin)
			json.NewEncoder(f).Encode(strings.Split(string(b), "\x00"))
		}
		f.Close()
	} else if len(os.Args) > 1 && os.Args[1] == "commit" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	if len(os.Args) > 1 && os.Args[1] == "info" {
		fmt.Print(`{"schema":"filees.native-svn/v1","ok":true,"entries":[{"path":".","url":"file:///lab","repos_root_url":"file:///lab","repos_uuid":"test-uuid","kind":"dir","revision":1,"last_changed_rev":1}]}`)
		return
	}
	if os.Getenv("FILEES_TEST_RA_SLEEP") == "1" {
		time.Sleep(2 * time.Second)
	}
	fmt.Print(os.Getenv("FILEES_TEST_RA_REPLY"))
}
func TestNativeRAOldHelperRefusedBeforeUpdate(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":1,"conflicts":[],"changes":[]}`)
	t.Setenv("FILEES_TEST_RA_OLD_HELPER", "1")
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	if _, e := c.nativeUpdate(t.Context(), t.TempDir(), nil, false); e == nil {
		t.Fatal("old helper accepted")
	}
	if _, e := c.nativeCheckout(t.Context(), "file:///repo", filepath.Join(t.TempDir(), "wc")); e == nil {
		t.Fatal("old helper accepted for checkout")
	}
	if _, e := c.nativeCheckoutDepthEmpty(t.Context(), "file:///repo", filepath.Join(t.TempDir(), "shelf")); e == nil {
		t.Fatal("old helper accepted for sparse checkout")
	}
	if _, e := os.Stat(trace); !os.IsNotExist(e) {
		t.Fatal("mutation attempted before capability check", e)
	}
}

func TestNativeSparseCheckoutUsesExplicitDepth(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":1,"conflicts":[],"changes":[]}`)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	if _, err := c.nativeCheckoutDepthEmpty(t.Context(), "file:///repo", filepath.Join(t.TempDir(), "shelf")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"checkout","--url","file:///repo"`) || !strings.Contains(string(data), `"--depth","empty"`) {
		t.Fatalf("native sparse checkout argv = %s", data)
	}
}

func TestNativeSparseFetchRequiresParentsFeatureAndTargetsOnePath(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":1,"conflicts":[],"changes":[]}`)
	wc := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	t.Setenv("FILEES_TEST_RA_OLD_HELPER", "1")
	if _, err := c.nativeFetchSparsePath(t.Context(), wc, "incoming/deep.txt"); err == nil {
		t.Fatal("old helper accepted sparse parent creation")
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("mutation attempted before sparse capability check", err)
	}
	t.Setenv("FILEES_TEST_RA_OLD_HELPER", "0")
	if _, err := c.nativeFetchSparsePath(t.Context(), wc, "incoming/deep.txt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"update","--wc"`) || !strings.Contains(string(data), `"--depth","empty","--parents","--","incoming/deep.txt"`) {
		t.Fatalf("native sparse fetch argv = %s", data)
	}
}

func TestNativeLogPreservesCommitDate(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"entries":[{"revision":7,"date":"2026-07-01T10:00:00.123456Z","message":"remote commit"}]}`)
	entries, err := c.nativeLog(t.Context(), "file:///lab", "7:7")
	if err != nil || len(entries) != 1 || entries[0].Date != "2026-07-01T10:00:00.123456Z" {
		t.Fatalf("date lost: %+v %v", entries, err)
	}
	if nativeWCOps(c) {
		mapped, err := c.LogMessages(t.Context(), "file:///lab", 7, 7)
		if err != nil || len(mapped) != 1 || mapped[0].Date != entries[0].Date {
			t.Fatalf("adapter date lost: %+v %v", mapped, err)
		}
	}
}
func raFake(t *testing.T, reply string) *execClient {
	t.Helper()
	p := fakeSVN(t, "native-ra")
	t.Setenv("FILEES_TEST_RA_REPLY", reply)
	return New(Options{NativeSVNPath: p, SvnPath: filepath.Join(t.TempDir(), "no-cli"), Timeout: time.Second}).(*execClient)
}
func TestNativeRAExactAndEmptyCommit(t *testing.T) {
	for _, r := range []string{"17", "null"} {
		t.Run(r, func(t *testing.T) {
			c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":`+r+`}`)
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("FILEES_TEST_RA_TRACE", trace)
			_, rev, e := c.nativeCommit(t.Context(), t.TempDir(), []string{"-Zażółć 新.txt"}, "shout 新", "marker", true)
			if e != nil {
				t.Fatal(e)
			}
			if (r == "null" && rev != 0) || (r == "17" && rev != 17) {
				t.Fatal(rev)
			}
			b, e := os.ReadFile(trace)
			if e != nil {
				t.Fatal(e)
			}
			s := string(b)
			for _, part := range []string{"--keep-locks", "filees:commit-id=marker", "shout 新", "-Zażółć 新.txt"} {
				if !strings.Contains(s, part) {
					t.Fatal(s)
				}
			}
		})
	}
}
func TestNativeRAInvalidReceiptsFailClosed(t *testing.T) {
	for _, r := range []string{`{}`, `{"schema":"wrong","ok":true,"revision":2}`, `{"schema":"filees.native-svn/v1","ok":true}`, `{"schema":"filees.native-svn/v1","ok":true,"revision":1.5}`, `{"schema":"filees.native-svn/v1","ok":true,"revision":-1}`} {
		c := raFake(t, r)
		if _, _, e := c.nativeCommit(t.Context(), t.TempDir(), []string{"a"}, "m", "", false); e == nil {
			t.Fatal(r)
		}
	}
}
func TestNativeRACommitDoesNotSplitOrPublishRoot(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":1}`)
	wc := t.TempDir()
	for _, paths := range [][]string{nil, {"."}, {".", "a"}, make([]string, nativeCommitTargetLimit+1), {"../escape"}} {
		for i := range paths {
			if paths[i] == "" {
				paths[i] = fmt.Sprint(i)
			}
		}
		if _, _, e := c.nativeCommit(t.Context(), wc, paths, "m", "", false); e == nil {
			t.Fatal(paths)
		}
	}
}
func TestNativeRALockMixedAndMalformed(t *testing.T) {
	raw := map[string]any{}
	json.Unmarshal([]byte(`{"locked":[{"path":"a","ok":false,"error":"held"},{"path":"b","ok":true,"error":null}]}`), &raw)
	results, e := nativeLockReceipt(raw, "lock", []string{"a", "b"})
	var fail *NativePathFailure
	if !errors.As(e, &fail) || len(results) != 2 || !results[1].OK {
		t.Fatalf("%v %v", results, e)
	}
	for _, r := range []string{`{}`, `{"locked":[]}`, `{"locked":[{"path":"a","ok":true},{"path":"a","ok":true}]}`, `{"locked":[{"path":"a","ok":false},{"path":"b","ok":true}]}`} {
		raw = map[string]any{}
		json.Unmarshal([]byte(r), &raw)
		if _, e := nativeLockReceipt(raw, "lock", []string{"a", "b"}); e == nil {
			t.Fatal(r)
		}
	}
}
func TestNativeRAConflictsAreStructured(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":3,"conflicts":["dir/新 name.txt"],"changes":[]}`)
	out, e := c.nativeUpdate(t.Context(), t.TempDir(), nil, false)
	if e != nil {
		t.Fatal(e)
	}
	p, ok := UpdateConflicts(out)
	if !ok || len(p) != 1 || p[0] != "dir/新 name.txt" {
		t.Fatal(out)
	}
	for _, r := range []string{`{"revision":3}`, `{"revision":3,"conflicts":["../escape"]}`} {
		raw := map[string]any{}
		json.Unmarshal([]byte(r), &raw)
		if _, e := nativeUpdateReceipt(raw); e == nil {
			t.Fatal(r)
		}
	}
}
func TestNativeRAPinsAndCancellation(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"entries":[]}`)
	if _, e := c.nativeLog(t.Context(), "svn+ssh://user@host/repo", "1:1"); e == nil || !strings.Contains(e.Error(), "pinned") {
		t.Fatal(e)
	}
	t.Setenv("FILEES_TEST_RA_SLEEP", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, e := c.nativeLog(ctx, "file:///lab", "1:1")
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestNativeRAWindowsDispatchNoFallback(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows routing only")
	}
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":2,"conflicts":[],"changes":[]}`)
	if _, e := c.Checkout(t.Context(), "file:///lab", filepath.Join(t.TempDir(), "new")); e != nil {
		t.Fatal(e)
	}
	c.nativeSVNPath = filepath.Join(t.TempDir(), "missing.exe")
	if _, e := c.Update(t.Context(), t.TempDir()); e == nil {
		t.Fatal("missing helper accepted")
	}
	if _, e := c.LockWithComment(t.Context(), t.TempDir(), []string{"a"}, "m", true); e == nil {
		t.Fatal("force enabled")
	}
}
