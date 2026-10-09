//go:build native_svn_probe

package client

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
	"github.com/google/uuid"
)

func TestNativeRemoteFileDeleteAtomicAndLostReply(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if !filepath.IsAbs(helper) {
		t.Fatal("absolute FILEES_SVN_PROBE required")
	}
	for _, mode := range []string{"normal", "before", "after", "after-foreign-head", "locked", "stale", "uuid", "non-root", "directory", "absent", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			repo, source, arm := filepath.Join(root, "repo"), filepath.Join(root, "import"), filepath.Join(root, "fault")
			run := func(tool string, args ...string) string {
				t.Helper()
				out, err := exec.CommandContext(t.Context(), nativeFixtureTool(t, tool), args...).CombinedOutput()
				if err != nil {
					t.Fatalf("fixture %s: %v %s", tool, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(os.MkdirAll(filepath.Join(source, "Dokumenty źródłowe"), 0700))
			must(os.WriteFile(filepath.Join(source, "Dokumenty źródłowe", "plik 新.txt"), []byte("kept in history"), 0600))
			must(os.WriteFile(filepath.Join(source, "keep.txt"), []byte("preserve unrelated"), 0600))
			run("svnadmin", "create", repo)
			rootURL := svnurl.File(repo)
			run("svn", "import", source, rootURL, "-m", "fixture", "--non-interactive")
			m := RemoteFileDelete{RootURL: rootURL, UUID: run("svnlook", "uuid", repo), BaseRevision: 1,
				OperationID: uuid.NewString(), Source: "Dokumenty źródłowe/plik 新.txt", Message: "Jawne usunięcie bez pobierania"}
			newClient := func(native string) *execClient {
				return New(Options{NativeSVNPath: native, SvnPath: filepath.Join(root, "absent-cli.exe")}).(*execClient)
			}
			c := newClient(helper)
			ctx := t.Context()
			wantHead := "1"
			switch mode {
			case "locked":
				// ASCII permits the independent Windows SVN CLI to lock this file.
				m.Source = "keep.txt"
				run("svn", "lock", rootURL+"/keep.txt", "-m", "other writer", "--non-interactive")
			case "stale":
				run("svn", "mkdir", rootURL+"/foreign", "-m", "new head", "--non-interactive")
				wantHead = "2"
			case "uuid":
				m.UUID = uuid.NewString()
			case "non-root":
				m.RootURL += "/Dokumenty%20%C5%BAr%C3%B3d%C5%82owe"
			case "directory":
				m.Source = "Dokumenty źródłowe"
			case "absent":
				m.Source = "missing.txt"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode == "before" || mode == "after" || mode == "after-foreign-head" {
				proxy, err := os.Executable()
				must(err)
				t.Setenv("FILEES_TEST_FAULT_REAL", helper)
				t.Setenv("FILEES_TEST_FAULT_ARM", arm)
				fault := "after"
				if mode == "before" {
					fault = "before"
				}
				must(os.WriteFile(arm, []byte(fault), 0600))
				c = newClient(proxy)
			}
			receipt, err := c.RemoteFileDelete(ctx, m)
			if mode == "normal" {
				if err != nil || receipt == nil || receipt.Revision != 2 {
					t.Fatal(receipt, err)
				}
			} else if err == nil || receipt != nil {
				t.Fatal("failure did not refuse or lose receipt", receipt, err)
			}
			if mode != "normal" && mode != "after" && mode != "after-foreign-head" {
				if run("svnlook", "youngest", repo) != wantHead {
					t.Fatal("refused request changed repository")
				}
				if mode == "before" {
					if r, err := newClient(helper).InspectRemoteFileDelete(t.Context(), m); err != nil || r != nil {
						t.Fatal(r, err)
					}
				}
				if mode == "stale" || mode == "uuid" || mode == "non-root" || mode == "directory" || mode == "absent" {
					if !errors.Is(err, ErrCommitNotSent) {
						t.Fatalf("pre-editor refusal missing not-sent proof: %v", err)
					}
				}
				return
			}
			if run("svnlook", "youngest", repo) != "2" {
				t.Fatal("delete did not make exactly one revision")
			}
			if mode == "after-foreign-head" {
				run("svn", "mkdir", rootURL+"/foreign", "-m", "independent actor", "--non-interactive")
			}
			for range 2 {
				r, err := newClient(helper).InspectRemoteFileDelete(t.Context(), m)
				if err != nil || r == nil || r.Revision != 2 {
					t.Fatal("lost reply not recovered", r, err)
				}
			}
			// svnlook output uses the Windows console code page, not UTF-8.
			// Read versioned history through the native reader, never a live WC.
			u, err := url.Parse(rootURL)
			must(err)
			u.Path += "/Dokumenty źródłowe"
			entries, err := newClient(helper).HistoryList(t.Context(), u.String(), 2)
			must(err)
			if len(entries) != 0 {
				t.Fatal("deleted file still present", entries)
			}
			u.Path += "/plik 新.txt"
			old := filepath.Join(root, "historical.txt")
			_, err = newClient(helper).HistoryFetchFile(t.Context(), u.String(), 1, old)
			must(err)
			bytes, err := os.ReadFile(old)
			must(err)
			if string(bytes) != "kept in history" {
				t.Fatal("historical bytes changed")
			}
			if run("svnlook", "cat", repo, "keep.txt") != "preserve unrelated" {
				t.Fatal("unrelated file changed")
			}
			if r, err := newClient(helper).RemoteFileDelete(t.Context(), m); r != nil || !errors.Is(err, ErrCommitNotSent) {
				t.Fatal("stale replay accepted", r, err)
			}
		})
	}
}
