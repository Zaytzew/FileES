//go:build native_svn_probe && windows

package nativesvnprobe

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Cut after public move7 has staged its mixed copy layers but before their
// atomic normalization. Recovery must preserve ACTUAL changes; new structural
// operations cannot be mistaken for copy layers created by the earlier move.
func TestBoundMixedTreeNormalizationResume(t *testing.T) {
	probe := tool(t, "FILEES_SVN_TREE_PROBE", "")
	for _, change := range []string{"none", "bytes", "property", "add", "delete", "foreign-copy", "root-property"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t, "outside.txt")
			src, dst := "source", "folder/new"
			old, new := filepath.Join(f.wc, src), filepath.Join(f.wc, filepath.FromSlash(dst))
			if err := os.Mkdir(old, 0700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(old, "local.txt"), "published\n")
			f.svnRun(t, "add", src)
			f.svnRun(t, "propset", "filees:published", "original", src+"/local.txt")
			f.svnRun(t, "commit", "-m", "source fixture")
			f.svnRun(t, "update")
			write(t, filepath.Join(f.wc, "outside.txt"), "unrelated HEAD")
			f.svnRun(t, "commit", "-m", "outside", "outside.txt")
			base := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL)))
			uuid := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "repos-uuid")))
			call := func(phase, revision string) ([]byte, error) {
				return execute(t, f.wc, f.probe, "local-tree", "--disposable-wc", f.wc,
					"--phase", phase, "--url", f.repoURL, "--uuid", uuid,
					"--base", base, "--revision", revision, src, dst)
			}
			if out, err := call("prepare", "0"); err != nil {
				t.Fatal(err, string(out))
			}
			f.svnRun(t, "move", f.repoURL+"/"+src, f.repoURL+"/"+dst, "-m", "remote receipt")
			revision := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL)))
			rename(t, old, new)
			out, err := execute(t, f.wc, probe, "crash-metadata", f.wc, src, dst, revision)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 {
				t.Fatal("wrong cut", err, string(out))
			}
			switch change {
			case "bytes":
				write(t, filepath.Join(new, "local.txt"), "later unsent edit\n")
			case "property":
				f.svnRun(t, "propset", "filees:later", "unsent", dst+"/local.txt")
			case "root-property":
				f.svnRun(t, "propset", "filees:later", "unsent", dst)
			case "add":
				write(t, filepath.Join(new, "added.txt"), "unsent\n")
				f.svnRun(t, "add", dst+"/added.txt")
			case "delete":
				f.svnRun(t, "delete", dst+"/local.txt")
			case "foreign-copy":
				f.svnRun(t, "copy", "outside.txt", dst+"/extra.txt")
			}
			before := f.status(t)
			remoteBefore := f.svnRun(t, "log", "--xml", f.repoURL)
			// A caller must not bypass normalization by jumping straight to ACK.
			if out, err := call("ack", revision); err == nil || !bytes.Equal(before, f.status(t)) {
				t.Fatal("unnormalized copy was acknowledged", err, string(out))
			}
			refuse := change == "add" || change == "delete" || change == "foreign-copy" || change == "root-property"
			out, err = call("stage", revision)
			if refuse {
				if err == nil || !bytes.Equal(before, f.status(t)) {
					t.Fatal("unsafe recovery changed the WC", err, string(out))
				}
			} else {
				if err != nil {
					t.Fatal(err, string(out))
				}
				// Repeated staging has no new copy, ACK or file effect.
				if out, err = call("stage", revision); err != nil {
					t.Fatal(err, string(out))
				}
				if out, err = call("ack", revision); err != nil {
					t.Fatal(err, string(out))
				}
				status := string(f.svnRun(t, "status", "--xml", dst))
				if strings.Contains(status, `copied="true"`) ||
					(change == "none" && strings.Contains(status, "<entry")) ||
					(change != "none" && !strings.Contains(status, `"modified"`)) {
					t.Fatal("copy layer or hidden ACTUAL changes", status)
				}
				if got := strings.TrimSpace(string(f.svnRun(t, "propget", "filees:published", dst+"/local.txt"))); got != "original" {
					t.Fatal("lost pristine properties", got)
				}
				if change == "bytes" {
					if got, err := os.ReadFile(filepath.Join(new, "local.txt")); err != nil || string(got) != "later unsent edit\n" {
						t.Fatal("lost bytes", err)
					}
				}
			}
			if !bytes.Equal(remoteBefore, f.svnRun(t, "log", "--xml", f.repoURL)) {
				t.Fatal("local recovery wrote to the repository")
			}
		})
	}
}
