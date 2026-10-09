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

	"golang.org/x/sys/windows"
)

// This measures a candidate API, NOT a production reconciler. In particular,
// tests deliberately demonstrate hidden UNSENT bytes/properties, refusal for
// sparse children, and unsafe queue failures for additions/missing files.
// Apparent success is an admission failure in
// the eventual product, never permission to retire its durable evidence.
func TestTreeMetadataPublicAPIBoundaries(t *testing.T) {
	runTreeMetadataBoundaries(t, false)
}

// Do not recursively acknowledge descendants that were NOT explicitly changed
// in the remote copyfrom/delete. Measure the public API's implicit copy layer.
func TestTreeMetadataRootsQueueBoundaries(t *testing.T) {
	runTreeMetadataBoundaries(t, true)
}

func TestTreeMetadataProbeRequiresDisposableScope(t *testing.T) {
	probe := tool(t, "FILEES_SVN_TREE_PROBE", "")
	f := newFixture(t, "outside.txt")
	before := f.status(t)
	for _, args := range [][]string{
		{"metadata", f.wc, "folder", "folder/child", "1"},
		{"metadata", f.wc, "../outside", "new", "1"},
		{"metadata", f.wc, "folder", "new", "0"},
		{"metadata", f.wc, "folder", "new", "1junk"},
		{"unknown", f.wc, "folder", "new", "1"},
	} {
		if out, err := execute(t, f.wc, probe, args...); err == nil {
			t.Fatalf("bad input admitted: %q %s", args, out)
		}
	}
	// A normal product marker is NOT the disposable experiment's permission.
	if err := os.Mkdir(filepath.Join(f.wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.wc, ".filees-native-probe")); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, f.wc, probe, "metadata", f.wc, "folder", "new", "1"); err == nil || !bytes.Contains(out, []byte("fixture path")) {
		t.Fatalf("product scope admitted to experiment: %v %s", err, out)
	}
	// The marker removal is the test setup, not a metadata operation.
	write(t, filepath.Join(f.wc, ".filees-native-probe"), "Disposable fixture; never add to a real WC.\n")
	if err := os.Remove(filepath.Join(f.wc, ".filees")); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t); !bytes.Equal(got, before) {
		t.Fatal("bad input mutated WC metadata")
	}
}

func runTreeMetadataBoundaries(t *testing.T, roots bool) {
	probe := tool(t, "FILEES_SVN_TREE_PROBE", "")
	for _, sparse := range []bool{false, true} {
		for _, change := range []string{"none", "later-bytes", "later-missing", "later-unversioned", "later-property", "later-add", "later-root-property", "later-delete"} {
			for _, crash := range []bool{false, true} {
				t.Run(map[bool]string{false: "local/", true: "sparse/"}[sparse]+change+map[bool]string{false: "/returned", true: "/process-exit"}[crash], func(t *testing.T) {
					f := newFixture(t, "outside.txt")
					src, dst := "źródło folder", "folder/nowy folder"
					old := filepath.Join(f.wc, src)
					new := filepath.Join(f.wc, filepath.FromSlash(dst))
					if err := os.MkdirAll(filepath.Join(old, "remote"), 0700); err != nil {
						t.Fatal(err)
					}
					body := []byte("published bytes\n")
					write(t, filepath.Join(old, "local.txt"), string(body))
					write(t, filepath.Join(old, "remote", "cloud.txt"), "remote-only bytes\n")
					f.svnRun(t, "add", src)
					f.svnRun(t, "propset", "filees:tree-fixture", "published", src+"/local.txt")
					f.svnRun(t, "commit", "-m", "mixed sparse source")
					f.svnRun(t, "update")
					if sparse {
						f.svnRun(t, "update", "--set-depth", "exclude", src+"/remote")
						// An unreadable ordinary stand-in at an excluded location proves
						// no content read of this object, not actual CFAPI hydration.
						if err := os.Mkdir(filepath.Join(old, "remote"), 0700); err != nil {
							t.Fatal(err)
						}
						write(t, filepath.Join(old, "remote", "cloud.txt"), "")
					}
					file := filepath.Join(old, "local.txt")
					beforeID := treeProbeID(t, file)
					beforeInfo, err := os.Stat(file)
					if err != nil {
						t.Fatal(err)
					}
					f.svnRun(t, "move", f.repoURL+"/"+src, f.repoURL+"/"+dst, "-m", "exact remote tree move")
					revision := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", f.repoURL)))
					remoteLog := f.svnRun(t, "log", "--xml", f.repoURL)
					if err := os.Rename(old, new); err != nil {
						t.Fatal(err)
					}
					cloud := filepath.Join(new, "remote", "cloud.txt")
					hold := windows.InvalidHandle
					if sparse {
						hold = treeProbeDenyRead(t, cloud)
						defer func() {
							if hold != windows.InvalidHandle {
								windows.CloseHandle(hold)
							}
						}()
					}
					mode := "metadata"
					if crash {
						mode = "crash-metadata"
					}
					out, err := execute(t, f.wc, probe, mode, f.wc, src, dst, revision)
					if crash {
						var exit *exec.ExitError
						if !errors.As(err, &exit) || exit.ExitCode() != 77 {
							t.Fatalf("wrong cut: %v %s", err, out)
						}
					} else if err != nil {
						t.Fatalf("metadata: %v %s", err, out)
					}
					file = filepath.Join(new, "local.txt")
					info, err := os.Stat(file)
					if err != nil || treeProbeID(t, file) != beforeID || !info.ModTime().Equal(beforeInfo.ModTime()) {
						t.Fatalf("metadata move replaced or wrote the local object: %v", err)
					}
					if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, body) {
						t.Fatalf("metadata move changed bytes: %v", err)
					}
					wantItem := "normal"
					switch change {
					case "later-bytes":
						body = []byte("later local edit\n")
						write(t, file, string(body))
						wantItem = "normal" // UNSAFE: the queue records current stat despite old pristine.
						if roots {
							wantItem = "modified"
						}
					case "later-missing":
						if err := os.Rename(file, filepath.Join(f.root, "retained.txt")); err != nil {
							t.Fatal(err)
						}
						wantItem = "missing"
					case "later-unversioned", "later-add":
						write(t, filepath.Join(new, "unsent.txt"), "never sent\n")
						if change == "later-add" {
							f.svnRun(t, "add", dst+"/unsent.txt")
						}
					case "later-property":
						f.svnRun(t, "propset", "filees:tree-fixture", "UNSENT", dst+"/local.txt")
					case "later-root-property":
						f.svnRun(t, "propset", "filees:tree-root-fixture", "UNSENT", dst)
					case "later-delete":
						f.svnRun(t, "delete", "--keep-local", dst+"/local.txt")
						wantItem = "unversioned"
						if roots {
							wantItem = "deleted"
						}
					}
					beforeACK := f.status(t)
					ack := "ack"
					if roots {
						ack = "ack-roots"
					}
					out, err = execute(t, f.wc, probe, ack, f.wc, src, dst, revision)
					if sparse && !roots {
						// The excluded directory is not locked by public legacy adm.
						// A failed DB transaction must preserve the pending metadata.
						if err == nil || !strings.Contains(string(out), "No write-lock") {
							t.Fatalf("sparse boundary changed: %v %s", err, out)
						}
						if got := f.status(t); !bytes.Equal(got, beforeACK) {
							t.Fatal("failed queue did not roll back WC metadata")
						}
					} else if change == "later-add" && !roots {
						if err == nil || !strings.Contains(string(out), "old_checksum != NULL") {
							t.Fatalf("addition boundary changed: %v %s", err, out)
						}
						if got := f.status(t); !bytes.Equal(got, beforeACK) {
							t.Fatal("rejected addition queue did not roll back metadata")
						}
					} else if change == "later-missing" && !roots {
						if err == nil || !strings.Contains(string(out), "Failed to run the WC DB work queue") {
							t.Fatalf("missing-file boundary changed: %v %s", err, out)
						}
						if got, err := os.ReadFile(filepath.Join(f.root, "retained.txt")); err != nil || !bytes.Equal(got, body) {
							t.Fatalf("missing-file recovery lost retained bytes: %v", err)
						}
						if _, err := os.Stat(file); !os.IsNotExist(err) {
							t.Fatal("queue recreated the intentionally missing file")
						}
						if rev := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", dst))); rev != revision {
							t.Fatalf("expected committed metadata before WQ failure: %q", rev)
						}
					} else if err != nil {
						t.Fatalf("post-commit queue: %v %s", err, out)
					}
					if change != "later-missing" {
						if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, body) || treeProbeID(t, file) != beforeID {
							t.Fatalf("ACK changed local bytes/object: %v", err)
						}
					}
					if sparse {
						if info, err := os.Stat(cloud); err != nil || info.Size() != 0 {
							t.Fatalf("ACK populated excluded stand-in: %v", err)
						}
						if f, err := os.Open(cloud); err == nil {
							f.Close()
							t.Fatal("read-denial guard did not cover the calls")
						}
					}
					if got := f.svnRun(t, "log", "--xml", f.repoURL); !bytes.Equal(got, remoteLog) {
						t.Fatal("metadata experiment wrote to the repository")
					}
					if !roots && (sparse || change == "later-add" || change == "later-missing") {
						return // Refusal, not a completed local ACK.
					}
					rows := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--", dst+"/local.txt")["entries"].([]any)
					if len(rows) != 1 || rows[0].(map[string]any)["item"] != wantItem {
						t.Fatalf("wrong byte state: %#v, want %s", rows, wantItem)
					}
					if change == "later-property" {
						// Recursive queue swallows the child's local property; roots-only
						// preserves it. Compare both with the actual unchanged remote prop.
						wantProps := "normal"
						if roots {
							wantProps = "modified"
						}
						if rows[0].(map[string]any)["props"] != wantProps {
							t.Fatalf("changed queue semantics; revisit admission: %#v", rows)
						}
						local := strings.TrimSpace(string(f.svnRun(t, "propget", "filees:tree-fixture", dst+"/local.txt")))
						remote := strings.TrimSpace(string(f.svnRun(t, "propget", "filees:tree-fixture", f.repoURL+"/"+dst+"/local.txt")))
						if local != "UNSENT" || remote != "published" {
							t.Fatalf("property boundary not measured: local=%q remote=%q", local, remote)
						}
					}
					if change == "later-bytes" {
						if got := f.svnRun(t, "cat", "-r", "BASE", dst+"/local.txt"); string(got) != "published bytes\n" {
							t.Fatalf("wrong pristine: %s", got)
						}
						// The recursive queue falsely calls changed bytes normal;
						// roots-only preserves their modified state and old pristine.
					}
					if change == "later-root-property" {
						root := f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--depth", "empty", "--", dst)["entries"].([]any)
						// Even roots-only swallows properties on its selected root.
						// The real coordinator must reject or separately preserve them.
						if len(root) != 1 || root[0].(map[string]any)["props"] != "normal" {
							t.Fatalf("root-property boundary changed: %#v", root)
						}
						if got := strings.TrimSpace(string(f.svnRun(t, "propget", "filees:tree-root-fixture", dst))); got != "UNSENT" {
							t.Fatalf("root property was overwritten: %q", got)
						}
						if remote := f.svnRun(t, "proplist", "--xml", f.repoURL+"/"+dst); bytes.Contains(remote, []byte("filees:tree-root-fixture")) {
							t.Fatal("unsent root property appeared on the server")
						}
					}
					if change == "later-add" || change == "later-unversioned" {
						rows = f.jsonCall(t, true, "status", "--disposable-wc", f.wc, "--", dst+"/unsent.txt")["entries"].([]any)
						want := "unversioned"
						if change == "later-add" {
							want = "added" // Roots-only must preserve the unsent addition.
						}
						if len(rows) != 1 || rows[0].(map[string]any)["item"] != want {
							t.Fatalf("addition boundary changed: %#v", rows)
						}
						if got, err := os.ReadFile(filepath.Join(new, "unsent.txt")); err != nil || string(got) != "never sent\n" {
							t.Fatalf("lost unsent bytes: %v", err)
						}
					}
					if rev := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", dst))); rev != revision {
						t.Fatalf("destination root has wrong revision: %q", rev)
					}
					if roots && change == "none" {
						if rev := strings.TrimSpace(string(f.svnRun(t, "info", "--show-item", "revision", dst+"/local.txt"))); rev != revision {
							t.Fatalf("implicit descendant BASE has wrong revision: %q", rev)
						}
						if sparse {
							// Move the test stand-in OUT, without deleting it. Then prove
							// the new sparse path can fetch its original copied contents.
							if err := windows.CloseHandle(hold); err != nil {
								t.Fatal(err)
							}
							hold = windows.InvalidHandle
							if err := os.Rename(filepath.Dir(cloud), filepath.Join(f.root, "stand-in")); err != nil {
								t.Fatal(err)
							}
							f.svnRun(t, "update", "--set-depth", "infinity", dst+"/remote")
							if got, err := os.ReadFile(cloud); err != nil || string(got) != "remote-only bytes\n" {
								t.Fatalf("moved excluded child cannot fetch: %v %q", err, got)
							}
						}
					}
				})
			}
		}
	}
}

func treeProbeID(t *testing.T, path string) [2]uint32 {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		t.Fatal(err)
	}
	return [2]uint32{info.FileIndexHigh, info.FileIndexLow}
}

func treeProbeDenyRead(t *testing.T, path string) windows.Handle {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// FILE_READ_ATTRIBUTES alone does not enter Windows' data sharing checks.
	// A data-capable handle on this ordinary EMPTY test stand-in holds the
	// denial; it never reads bytes and is not a proposed CFAPI product guard.
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
