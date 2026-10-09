//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundLocalTreePreflightRefusesForeignBindings(t *testing.T) {
	f := newFixture(t, "outside.txt")
	src, dst := "source", "folder/new"
	if err := os.MkdirAll(filepath.Join(f.wc, src), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.wc, src, "local.txt"), "original")
	f.svnRun(t, "add", src)
	f.svnRun(t, "commit", "-m", "fixture source preparation")
	write(t, filepath.Join(f.wc, "outside.txt"), "advance repository outside source")
	f.svnRun(t, "commit", "-m", "advance fixture revision")
	f.svnRun(t, "update")
	base := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", src)))
	id := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "repos-uuid")))
	for _, bad := range []string{"uuid", "url", "base", "properties", "mixed"} {
		t.Run(bad, func(t *testing.T) {
			root, uuid, rev := f.repoURL, id, base
			switch bad {
			case "uuid":
				uuid = "11111111-1111-4111-8111-111111111111"
			case "url":
				root += "/folder"
			case "base":
				rev = "1"
			case "properties":
				f.svnRun(t, "propset", "filees:unsent", "yes", src)
			case "mixed":
				f.svnRun(t, "update", "-r", "2", src+"/local.txt")
			}
			before := f.status(t)
			out, err := execute(t, f.wc, f.probe, "local-tree", "--disposable-wc", f.wc, "--phase", "inspect", "--url", root, "--uuid", uuid, "--base", rev, "--revision", "0", src, dst)
			if err == nil || !bytes.Equal(before, f.status(t)) {
				t.Fatal("unsafe preflight", err, string(out))
			}
			if bad == "properties" {
				f.svnRun(t, "propdel", "filees:unsent", src)
			}
			if bad == "mixed" {
				f.svnRun(t, "update", src+"/local.txt")
			}
		})
	}
}

// These are real WC metadata phases against an independently committed remote
// copy/delete. Physical moves are ordinary lab renames, not a CFAPI acceptance.
func TestBoundLocalTreeMetadata(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		for _, change := range []string{"none", "bytes", "missing", "unversioned", "property", "add", "delete", "root-property"} {
			t.Run(map[bool]string{false: "local/", true: "sparse/"}[sparse]+change, func(t *testing.T) {
				f := newFixture(t, "outside.txt")
				src, dst := "źródło", "folder/cel"
				old, new := filepath.Join(f.wc, src), filepath.Join(f.wc, filepath.FromSlash(dst))
				if err := os.MkdirAll(filepath.Join(old, "remote"), 0700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(old, "local.txt"), "original\n")
				write(t, filepath.Join(old, "remote", "cloud.txt"), "cloud bytes\n")
				f.svnRun(t, "add", src)
				f.svnRun(t, "commit", "-m", "source")
				f.svnRun(t, "update")
				base := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", src)))
				uuid := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "repos-uuid")))
				if sparse {
					f.svnRun(t, "update", "--set-depth", "exclude", src+"/remote")
				}
				call := func(phase, revision string, ok bool) []byte {
					t.Helper()
					out, err := execute(t, f.wc, f.probe, "local-tree", "--disposable-wc", f.wc, "--phase", phase,
						"--url", f.repoURL, "--uuid", uuid, "--base", base, "--revision", revision, src, dst)
					if (err == nil) != ok {
						t.Fatalf("%s: %v %s", phase, err, out)
					}
					return out
				}
				call("inspect", "0", true)
				f.svnRun(t, "move", f.repoURL+"/"+src, f.repoURL+"/"+dst, "-m", "remote copy/delete")
				revision := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL)))
				rename(t, old, new)
				call("stage", revision, true)
				switch change {
				case "bytes":
					write(t, filepath.Join(new, "local.txt"), "later edit\n")
				case "missing":
					if err := os.Remove(filepath.Join(new, "local.txt")); err != nil {
						t.Fatal(err)
					}
				case "unversioned":
					write(t, filepath.Join(new, "new.txt"), "not published\n")
				case "property":
					f.svnRun(t, "propset", "filees:test", "unsent", dst+"/local.txt")
				case "root-property":
					f.svnRun(t, "propset", "filees:test", "unsent", dst)
				case "add":
					write(t, filepath.Join(new, "new.txt"), "not published\n")
					f.svnRun(t, "add", dst+"/new.txt")
				case "delete":
					f.svnRun(t, "delete", dst+"/local.txt")
				}
				if change == "root-property" {
					before := f.status(t)
					call("ack", revision, false)
					if !bytes.Equal(f.status(t), before) {
						t.Fatal("root property refusal changed metadata")
					}
					return
				}
				call("ack", revision, true)
				for _, phase := range []string{"verify", "stage", "ack"} {
					if out := call(phase, revision, true); !strings.Contains(string(out), `"state":"complete"`) {
						t.Fatal("completion was not idempotent", string(out))
					}
				}
				status := string(f.svnRun(t, "status", "--xml", dst))
				want := map[string]string{"bytes": "modified", "missing": "missing", "unversioned": "unversioned", "property": "modified", "add": "added", "delete": "deleted"}[change]
				if want != "" && !strings.Contains(status, `"`+want+`"`) {
					t.Fatalf("unsent %s hidden: %s", change, status)
				}
				if sparse {
					f.svnRun(t, "update", "--set-depth", "infinity", dst+"/remote")
					body, err := os.ReadFile(filepath.Join(new, "remote", "cloud.txt"))
					if err != nil || string(body) != "cloud bytes\n" {
						t.Fatalf("sparse child lost: %v %q", err, body)
					}
				}
			})
		}
	}
}
