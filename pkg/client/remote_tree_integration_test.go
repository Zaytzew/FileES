//go:build native_svn_probe

package client

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filees/internal/svnurl"
	"github.com/google/uuid"
)

func TestNativeRemoteTreeMoveAtomicAndLostReply(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if !filepath.IsAbs(helper) {
		t.Fatal("absolute FILEES_SVN_PROBE required")
	}
	for _, mode := range []string{"normal", "before", "after", "after-foreign-head"} {
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
			must(os.MkdirAll(filepath.Join(source, "Dokumenty źródłowe", "Załączniki 新"), 0700))
			must(os.MkdirAll(filepath.Join(source, "01_PROJEKT"), 0700))
			must(os.WriteFile(filepath.Join(source, "Dokumenty źródłowe", "Załączniki 新", "a.txt"), []byte("unchanged content"), 0600))
			must(os.WriteFile(filepath.Join(source, "Dokumenty źródłowe", "empty.txt"), nil, 0600))
			run("svnadmin", "create", repo)
			rootURL := svnurl.File(repo)
			run("svn", "import", source, rootURL, "-m", "fixture", "--non-interactive")
			m := RemoteTreeMove{RootURL: rootURL, UUID: run("svnlook", "uuid", repo), BaseRevision: 1,
				OperationID: uuid.NewString(), Source: "Dokumenty źródłowe", Destination: "01_PROJEKT/Dokumenty źródłowe", Message: "Przeniesienie bez pobierania"}
			newClient := func(native string) *execClient {
				return New(Options{NativeSVNPath: native, SvnPath: filepath.Join(root, "absent-cli.exe")}).(*execClient)
			}
			c := newClient(helper)
			if mode != "normal" {
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
			receipt, err := c.RemoteTreeMove(t.Context(), m)
			if mode == "normal" {
				if err != nil || receipt == nil || receipt.Revision != 2 {
					t.Fatal(receipt, err)
				}
			} else if err == nil || receipt != nil {
				t.Fatal("fault did not lose receipt", receipt, err)
			}
			if mode == "before" {
				if r, err := newClient(helper).InspectRemoteTreeMove(t.Context(), m); err != nil || r != nil {
					t.Fatal(r, err)
				}
				if run("svnlook", "youngest", repo) != "1" {
					t.Fatal("before fault changed repo")
				}
				return
			}
			if run("svnlook", "youngest", repo) != "2" {
				t.Fatal("move did not make exactly one revision")
			}
			if mode == "after-foreign-head" {
				run("svn", "mkdir", rootURL+"/foreign", "-m", "independent actor", "--non-interactive")
			}
			for range 2 { // Fresh client, read-only recovery, not a replay.
				r, err := newClient(helper).InspectRemoteTreeMove(t.Context(), m)
				if err != nil || r == nil || r.Revision != 2 {
					t.Fatal("lost reply not recovered", r, err)
				}
			}
			// The independent Windows svnlook argv is ANSI and cannot name 新.
			// Use the existing raw-byte native history reader as content oracle;
			// recovery above independently checks both copyfrom and delete paths.
			contentURL, err := url.Parse(rootURL)
			must(err)
			contentURL.Path += "/" + m.Destination + "/Załączniki 新/a.txt"
			out := filepath.Join(root, "verified.txt")
			_, err = newClient(helper).HistoryFetchFile(t.Context(), contentURL.String(), 2, out)
			must(err)
			bytes, err := os.ReadFile(out)
			must(err)
			if string(bytes) != "unchanged content" {
				t.Fatal("contents changed")
			}
			// Repeating the stale operation is refused before an editor, even if
			// the caller mistakenly tries to replay a successful/lost response.
			if _, err := newClient(helper).RemoteTreeMove(t.Context(), m); !errors.Is(err, ErrCommitNotSent) {
				t.Fatal("stale replay not refused before effect", err)
			}
			t.Log("atomic remote Unicode folder move, copyfrom r1, unchanged bytes and exact read-only receipt PASS")
		})
	}
}

func TestNativeRemoteTreeMoveRefusalsAndFiles(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if !filepath.IsAbs(helper) {
		t.Fatal("absolute FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, source := filepath.Join(root, "repo"), filepath.Join(root, "import")
	run := func(tool string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), nativeFixtureTool(t, tool), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "existing.txt", "literal%name@1.txt"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("svnadmin", "create", repo)
	rootURL := svnurl.File(repo)
	run("svn", "import", source, rootURL, "-m", "fixture", "--non-interactive")
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli.exe")}).(*execClient)
	m := RemoteTreeMove{RootURL: rootURL, UUID: run("svnlook", "uuid", repo), BaseRevision: 1,
		OperationID: uuid.NewString(), Source: "a.txt", Destination: "b.txt", Message: "Move one file"}
	for _, change := range []func(*RemoteTreeMove){
		func(m *RemoteTreeMove) { m.UUID = uuid.NewString() },
		func(m *RemoteTreeMove) { m.BaseRevision = 0 },
		func(m *RemoteTreeMove) { m.Destination = "existing.txt" },
		func(m *RemoteTreeMove) { m.Source = "missing.txt" },
		func(m *RemoteTreeMove) { m.Destination = "missing-parent/b.txt" },
		func(m *RemoteTreeMove) { m.RootURL += "/a.txt" },
	} {
		bad := m
		change(&bad)
		if _, err := c.RemoteTreeMove(t.Context(), bad); !errors.Is(err, ErrCommitNotSent) {
			t.Fatal("unproven pre-effect refusal", err)
		}
		if run("svnlook", "youngest", repo) != "1" {
			t.Fatal("refusal changed repo")
		}
	}
	if r, err := c.RemoteTreeMove(t.Context(), m); err != nil || r.Revision != 2 {
		t.Fatal(r, err)
	}
	m.Source, m.Destination, m.BaseRevision, m.OperationID = "literal%name@1.txt", "now%name@2.txt", 2, uuid.NewString()
	if r, err := c.RemoteTreeMove(t.Context(), m); err != nil || r.Revision != 3 {
		t.Fatal(r, err)
	}
	if r, err := c.InspectRemoteTreeMove(t.Context(), m); err != nil || r.Revision != 3 {
		t.Fatal(r, err)
	}
	if run("svnlook", "cat", repo, m.Destination) != "literal%name@1.txt" {
		t.Fatal("literal percent/peg name altered")
	}
	// Locked source: never steal or silently omit a required token. The public
	// editor keeps server lock checks; this primitive intentionally sends none.
	run("svn", "lock", rootURL+"/b.txt", "-m", "fixture owner", "--non-interactive")
	blocked := m
	blocked.Source, blocked.Destination, blocked.BaseRevision, blocked.OperationID = "b.txt", "locked-moved.txt", 3, uuid.NewString()
	if r, err := c.RemoteTreeMove(t.Context(), blocked); err == nil || r != nil {
		t.Fatal("locked source moved", r, err)
	}
	if run("svnlook", "youngest", repo) != "3" {
		t.Fatal("lock refusal changed repository")
	}
	if r, err := c.InspectRemoteTreeMove(t.Context(), blocked); err != nil || r != nil {
		t.Fatal("lock refusal has receipt", r, err)
	}
	run("svn", "unlock", rootURL+"/b.txt", "--non-interactive")
	// Hook refusal happens after opening the editor. It is an error, never a
	// confirmed success or permission to automatically repeat the mutation.
	hook, script := "pre-commit", "#!/bin/sh\nexit 1\n"
	if runtime.GOOS == "windows" {
		hook, script = "pre-commit.bat", "@echo off\r\nexit /b 1\r\n"
	}
	if err := os.WriteFile(filepath.Join(repo, "hooks", hook), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	blocked.OperationID = uuid.NewString()
	if r, err := c.RemoteTreeMove(t.Context(), blocked); err == nil || r != nil || errors.Is(err, ErrCommitNotSent) {
		t.Fatal("post-editor refusal misclassified", r, err)
	}
	if run("svnlook", "youngest", repo) != "3" {
		t.Fatal("hook refusal changed repository")
	}
	if r, err := c.InspectRemoteTreeMove(t.Context(), blocked); err != nil || r != nil {
		t.Fatal("hook refusal has receipt", r, err)
	}
}
