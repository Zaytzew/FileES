//go:build native_svn_probe

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/internal/svnurl"
)

// The test executable doubles as a transparent process proxy. Only a commit
// with an armed, disposable fault file is interrupted; real C does all SVN work.
// This models lost process output AFTER C has returned, not an RA disconnect
// before WC post-commit processing. It must not be counted as that latter test.
func init() {
	real := os.Getenv("FILEES_TEST_FAULT_REAL")
	if real == "" || len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-test.") {
		return
	}
	fault := os.Getenv("FILEES_TEST_FAULT_ARM")
	mode := ""
	if os.Args[1] == "commit" {
		if b, err := os.ReadFile(fault); err == nil {
			if err := os.Remove(fault); err != nil {
				panic(err)
			}
			mode = string(b)
		}
	}
	if mode == "before" {
		fmt.Fprintln(os.Stderr, "injected failure before C commit")
		os.Exit(86)
	}
	cmd := exec.Command(real, os.Args[1:]...)
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		os.Stdout.Write(output.Bytes())
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
		panic(err)
	}
	if mode == "after" {
		if err := os.WriteFile(fault+".receipt", output.Bytes(), 0600); err != nil {
			panic(err)
		}
		fmt.Fprintln(os.Stderr, "injected lost reply after C commit success")
		os.Exit(87)
	}
	os.Stdout.Write(output.Bytes())
	os.Exit(0)
}

// Characterization, NOT a green acceptance of receipt recovery. A passing
// test proves the documented gap: effect exists, API loses its exact receipt.
func TestNativeCommitLostReplyCharacterization(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if !filepath.IsAbs(helper) {
		t.Fatal("absolute FILEES_SVN_PROBE required")
	}
	proxy, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"before", "after", "after-foreign-head"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			repo, wc, arm := filepath.Join(root, "repo"), filepath.Join(root, "wc"), filepath.Join(root, "fault")
			run := func(tool string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), tool, args...)
				cmd.Env = svnProcessEnvironment(os.Environ(), "")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s: %v %s", tool, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("svnadmin", "create", repo)
			url := svnurl.File(repo)
			t.Setenv("FILEES_TEST_FAULT_REAL", helper)
			t.Setenv("FILEES_TEST_FAULT_ARM", arm)
			newClient := func() *execClient {
				return New(Options{NativeSVNPath: proxy, SvnPath: filepath.Join(root, "absent-cli"), Timeout: 20 * time.Second}).(*execClient)
			}
			c := newClient()
			if _, err := c.nativeCheckout(t.Context(), url, wc); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(wc, ".filees"), 0700); err != nil {
				t.Fatal(err)
			}
			path := "receipt.txt"
			if err := os.WriteFile(filepath.Join(wc, path), []byte("one durable publication\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.nativeAdd(t.Context(), wc, []string{path}); err != nil {
				t.Fatal(err)
			}
			commit := func(c *execClient) (int64, error) {
				// Public dispatch is Windows-only by contract. OpenBSD tests
				// the same native adapter directly, without changing that policy.
				if runtime.GOOS == "windows" {
					_, rev, err := c.CommitWithRevision(t.Context(), wc, url, []string{path}, "fault acceptance", false)
					return rev, err
				}
				_, rev, err := c.nativeCommit(t.Context(), wc, []string{path}, "fault acceptance", "openbsd-test-marker", false)
				return rev, err
			}
			armed := "after"
			if mode == "before" {
				armed = "before"
			}
			if err := os.WriteFile(arm, []byte(armed), 0600); err != nil {
				t.Fatal(err)
			}
			rev, err := commit(c)
			if err == nil || rev != 0 {
				t.Fatalf("fault not observed: rev=%d err=%v", rev, err)
			}
			t.Logf("first API result: revision=%d error=%v", rev, err)
			expectedHead := "0"
			if mode != "before" {
				expectedHead = "1"
				b, err := os.ReadFile(arm + ".receipt")
				var receipt struct {
					OK       bool
					Revision int64
				}
				if err != nil || json.Unmarshal(b, &receipt) != nil || !receipt.OK || receipt.Revision != 1 {
					t.Fatalf("missing successful C receipt: %s %v", b, err)
				}
				if got := run("svnlook", "cat", "-r", "1", repo, path); got != "one durable publication" {
					t.Fatal(got)
				}
				marker := run("svnlook", "propget", "--revprop", "-r", "1", repo, "filees:commit-id")
				if marker == "" {
					t.Fatal("missing server marker")
				}
				entries, err := c.nativeLog(t.Context(), url, "HEAD:1", "--revprop", "filees:commit-id")
				if err != nil || len(entries) != 1 || entries[0].Revprops["filees:commit-id"] != marker {
					t.Fatal("receipt not recoverable by marker", entries, err)
				}
				t.Log("C receipt r1 and transaction marker are present; normal API did not recover them")
			}
			if got := run("svnlook", "youngest", repo); got != expectedHead {
				t.Fatal("wrong effect", got)
			}
			if mode == "after-foreign-head" {
				// Independent CLI actor is fixture setup, never client fallback.
				run("svn", "mkdir", url+"/foreign", "-m", "independent actor", "--non-interactive")
				expectedHead = "2"
			}
			// A fresh client discards all in-memory state, like a restart.
			rev, err = commit(newClient())
			if err != nil {
				t.Fatal("retry failed", err)
			}
			if mode == "before" {
				if rev != 1 {
					t.Fatal("retry failed to publish", rev)
				}
				expectedHead = "1"
			} else if rev != 0 {
				t.Fatalf("unexpected recovered/foreign revision: %d", rev)
			}
			if got := run("svnlook", "youngest", repo); got != expectedHead {
				t.Fatal("duplicate transaction", got)
			}
			t.Logf("retry API revision=%d; final HEAD=%s; no duplicate. Receipt recovery acceptance=%v", rev, expectedHead, mode == "before")
		})
	}
}
