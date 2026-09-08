//go:build native_svn_probe

package nativesvnprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"filees/pkg/pathownership"
)

// Opt-in suite: missing tools are a failure, not an acceptance-looking skip.
func tool(t *testing.T, key, fallback string) string {
	t.Helper()
	p := os.Getenv(key)
	if p == "" {
		p = fallback
	}
	if p == "" {
		t.Fatalf("%s must point to the experimental executable", key)
	}
	p, err := exec.LookPath(p)
	if err != nil {
		t.Fatal(err)
	}
	p, err = filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func execute(t *testing.T, dir, program string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if runtime.GOOS != "windows" {
		cmd.Env = append(cmd.Env, "LC_ALL=C.UTF-8")
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("command timed out: %s %q: %s", program, args, out)
	}
	return out, err
}

type fixture struct{ root, wc, repoURL, svn, probe string }

func (f fixture) status(t *testing.T) []byte {
	t.Helper()
	// APR hash iteration changes XML attribute order between SVN processes.
	d := xml.NewDecoder(bytes.NewReader(f.svnRun(t, "status", "--xml")))
	var out bytes.Buffer
	e := xml.NewEncoder(&out)
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok {
			sort.Slice(start.Attr, func(i, j int) bool { return start.Attr[i].Name.Local < start.Attr[j].Name.Local })
			token = start
		}
		if err := e.EncodeToken(token); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func (f fixture) svnRun(t *testing.T, args ...string) []byte {
	t.Helper()
	args = append([]string{"--non-interactive", "--no-auth-cache", "--config-dir", filepath.Join(f.root, "config")}, args...)
	out, err := execute(t, f.wc, f.svn, args...)
	if err != nil {
		t.Fatalf("svn %q: %v\n%s", args, err, out)
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func rename(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T, old string) fixture {
	t.Helper()
	f := fixture{root: t.TempDir(), svn: tool(t, "FILEES_PROBE_SVN", "svn"), probe: tool(t, "FILEES_SVN_PROBE", "")}
	f.wc = filepath.Join(f.root, "wc")
	repo := filepath.Join(f.root, "repo")
	out, err := execute(t, f.root, tool(t, "FILEES_PROBE_SVNADMIN", "svnadmin"), "create", repo)
	if err != nil {
		t.Fatalf("svnadmin: %v %s", err, out)
	}
	p := filepath.ToSlash(repo)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	f.repoURL = (&url.URL{Scheme: "file", Path: p}).String()
	if err := os.Mkdir(f.wc, 0700); err != nil {
		t.Fatal(err)
	}
	f.svnRun(t, "checkout", f.repoURL, ".")
	if err := os.Mkdir(filepath.Join(f.wc, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.wc, old), "first version\n")
	write(t, filepath.Join(f.wc, "occupied.txt"), "another object\n")
	f.svnRun(t, "add", "--", old+"@", "occupied.txt", "folder")
	f.svnRun(t, "propset", "filees:probe", "base", "--", old+"@")
	f.svnRun(t, "commit", "--username", "creator", "-m", "birth")
	f.svnRun(t, "update")
	write(t, filepath.Join(f.wc, ".filees-native-probe"), "Disposable fixture; never add to a real WC.\n")
	return f
}

func (f fixture) call(t *testing.T, ok bool, args ...string) {
	t.Helper()
	out, err := execute(t, f.wc, f.probe, args...)
	var result struct {
		Schema string `json:"schema"`
		OK     bool   `json:"ok"`
		Errors []struct {
			Code    int
			Message string
		} `json:"errors"`
	}
	if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
		t.Fatalf("invalid probe JSON: %v\n%s", jsonErr, out)
	}
	if result.Schema != "filees.native-svn/v1" || result.OK != ok || (err == nil) != ok {
		t.Fatalf("probe expected ok=%v: err=%v output=%s", ok, err, out)
	}
	if !ok && (len(result.Errors) == 0 || result.Errors[0].Code == 0 || result.Errors[0].Message == "") {
		t.Fatalf("missing structured diagnostic: %s", out)
	}
}

func (f fixture) move(t *testing.T, ok bool, old, dst string) {
	t.Helper()
	f.call(t, ok, "record-move", "--disposable-wc", f.wc, old, dst)
}

func (f fixture) history(t *testing.T) []pathownership.Revision {
	t.Helper()
	raw := f.svnRun(t, "log", "--xml", "--verbose", "--quiet", "-r", "1:HEAD", f.repoURL)
	revs, err := pathownership.ParseLog(raw)
	if err != nil {
		t.Fatal(err)
	}
	return revs
}

func object(t *testing.T, f fixture, path string) pathownership.Object {
	t.Helper()
	revs := f.history(t)
	snap, err := pathownership.Replay(context.Background(), "fixture", int64(len(revs)), revs)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snap.Entries {
		if entry.Path == path {
			return entry.Object
		}
	}
	t.Fatalf("object missing: %s", path)
	return pathownership.Object{}
}

// Include bytes, symlink targets, modes and file mtimes; never follow links.
func dataTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".svn" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			content = []byte(link)
		} else {
			content, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[rel] = fmt.Sprintf("%x/%v/%d", sha256.Sum256(content), info.Mode(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestEditedMoveAncestryAndFork(t *testing.T) {
	for _, names := range [][2]string{{"old.txt", "new.txt"}, {"Łódź stara.txt", unicodeDestination()}, {"at@old.txt", "at@new.txt"}} {
		t.Run(names[0], func(t *testing.T) {
			old, dst := names[0], names[1]
			f := newFixture(t, old)
			f.call(t, true, "--version")
			original := object(t, f, old)
			// Property edits and content edits must survive the metadata-only call.
			f.svnRun(t, "propset", "filees:probe", "locally-edited", "--", old+"@")
			write(t, filepath.Join(f.wc, old), "uncommitted work before move\n")
			rename(t, filepath.Join(f.wc, old), filepath.Join(f.wc, dst))
			write(t, filepath.Join(f.wc, dst), "uncommitted work AFTER move\x00\xff\n")
			before := dataTree(t, f.wc)
			f.move(t, true, old, dst)
			if !reflect.DeepEqual(before, dataTree(t, f.wc)) {
				t.Fatal("move changed data, mode or mtime")
			}
			status := f.status(t)
			if !bytes.Contains(status, []byte("moved-from=")) || !bytes.Contains(status, []byte("moved-to=")) {
				t.Fatalf("no local move tracking: %s", status)
			}
			// New process, same request: a refusal, not a second application.
			f.move(t, false, old, dst)
			if !bytes.Equal(status, f.status(t)) || !reflect.DeepEqual(before, dataTree(t, f.wc)) {
				t.Fatal("retry changed scheduled state or data")
			}
			f.svnRun(t, "commit", "--username", "administrator", "-m", "administrative move")
			if got := object(t, f, dst); got != original {
				t.Fatalf("lost continuity: %+v != %+v", got, original)
			}
			if got := f.svnRun(t, "propget", "filees:probe", "--", dst+"@"); strings.TrimSpace(string(got)) != "locally-edited" {
				t.Fatalf("lost property: %s", got)
			}
			if got := f.svnRun(t, "cat", "--", f.repoURL+"/"+(&url.URL{Path: dst}).EscapedPath()+"@"); !bytes.Equal(got, []byte("uncommitted work AFTER move\x00\xff\n")) {
				t.Fatalf("lost committed bytes: %q", got)
			}
			f.svnRun(t, "copy", "--", dst+"@", "fork.txt")
			f.svnRun(t, "commit", "--username", "forker", "-m", "fork alongside source")
			fork := object(t, f, "fork.txt")
			if fork.ID == original.ID || fork.FirstCommitter != "forker" || fork.CreatedRevision != 3 {
				t.Fatalf("wrong fork: %+v", fork)
			}
			f.svnRun(t, "delete", "--", dst+"@")
			f.svnRun(t, "commit", "--username", "administrator", "-m", "later source deletion")
			if got := object(t, f, "fork.txt"); got != fork {
				t.Fatalf("later delete changed fork: %+v", got)
			}
		})
	}
}

func TestRejectedMovesPreserveFixture(t *testing.T) {
	cases := []string{"source-present", "destination-missing", "destination-versioned", "destination-added", "directory", "no-marker", "traversal", "absolute", "metadata", "alias", "same-path", "cross-wc", "symlink-destination", "symlink-parent", "unknown-verb"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "old.txt")
			old, dst := "old.txt", "new.txt"
			if name != "source-present" {
				rename(t, filepath.Join(f.wc, old), filepath.Join(f.wc, dst))
			} else {
				write(t, filepath.Join(f.wc, dst), "copy, not a move")
			}
			switch name {
			case "destination-missing":
				dst = "absent.txt"
			case "destination-versioned":
				dst = "occupied.txt"
			case "destination-added":
				f.svnRun(t, "add", dst)
			case "directory":
				old, dst = "folder", "new-folder"
				rename(t, filepath.Join(f.wc, old), filepath.Join(f.wc, dst))
			case "no-marker":
				if err := os.Remove(filepath.Join(f.wc, ".filees-native-probe")); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				dst = "../escape.txt"
			case "absolute":
				dst = filepath.ToSlash(filepath.Join(f.wc, "new.txt"))
			case "metadata":
				dst = ".SVN/wc.db"
			case "alias":
				dst = ".svn./wc.db"
			case "same-path":
				dst = old
			case "cross-wc":
				f.svnRun(t, "checkout", f.repoURL, "nested")
				rename(t, filepath.Join(f.wc, dst), filepath.Join(f.wc, "nested", dst))
				dst = "nested/" + dst
			case "symlink-destination", "symlink-parent":
				linkTarget := filepath.Join(f.wc, "new.txt")
				if name == "symlink-parent" {
					linkTarget = f.wc
				}
				if err := os.Symlink(linkTarget, filepath.Join(f.wc, "link")); err != nil {
					t.Fatalf("symlink fixture unavailable; acceptance incomplete: %v", err)
				}
				dst = "link"
				if name == "symlink-parent" {
					dst = "link/new.txt"
				}
			}
			before, status := dataTree(t, f.wc), f.status(t)
			if name == "unknown-verb" {
				f.call(t, false, "commit")
			} else {
				f.move(t, false, old, dst)
			}
			if !reflect.DeepEqual(before, dataTree(t, f.wc)) || !bytes.Equal(status, f.status(t)) {
				t.Fatalf("rejected request changed fixture data or SVN status\ndata before=%v\ndata after=%v\nstatus before=%s\nstatus after=%s", before, dataTree(t, f.wc), status, f.svnRun(t, "status", "--xml"))
			}
		})
	}
}

func TestNeedsLockMovePreservesLocalMode(t *testing.T) {
	f := newFixture(t, "old.txt")
	original := object(t, f, "old.txt")
	f.svnPropsetFromFile(t, "svn:needs-lock", "*", "old.txt")
	f.svnRun(t, "commit", "--username", "administrator", "-m", "editing policy")
	f.svnRun(t, "update")
	// Model FileES local owner access, independently of server reservation.
	if err := os.Chmod(filepath.Join(f.wc, "old.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.wc, "old.txt"), "owner edit\n")
	rename(t, filepath.Join(f.wc, "old.txt"), filepath.Join(f.wc, "new.txt"))
	before := dataTree(t, f.wc)
	f.move(t, true, "old.txt", "new.txt")
	if !reflect.DeepEqual(before, dataTree(t, f.wc)) {
		t.Fatal("metadata move changed local mode or bytes")
	}
	if got := f.svnRun(t, "propget", "svn:needs-lock", "new.txt"); strings.TrimSpace(string(got)) != "*" {
		t.Fatal("lost needs-lock")
	}
	f.svnRun(t, "commit", "--username", "administrator", "-m", "move")
	if got := object(t, f, "new.txt"); got != original {
		t.Fatal("lost continuity with needs-lock")
	}
}

func TestMissingSpecialSourceRefused(t *testing.T) {
	f := newFixture(t, "old.txt")
	// SVN stores symlinks as file nodes with svn:special; node kind alone
	// cannot distinguish a missing symlink from a missing regular file.
	if err := os.Symlink("old.txt", filepath.Join(f.wc, "special")); err != nil {
		t.Fatalf("symlink fixture unavailable; acceptance incomplete: %v", err)
	}
	f.svnRun(t, "add", "special")
	f.svnRun(t, "commit", "--username", "creator", "-m", "special node")
	if err := os.Remove(filepath.Join(f.wc, "special")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.wc, "new.txt"), "regular replacement is not symlink continuity")
	before, status := dataTree(t, f.wc), f.status(t)
	f.move(t, false, "special", "new.txt")
	if !reflect.DeepEqual(before, dataTree(t, f.wc)) || !bytes.Equal(status, f.status(t)) {
		t.Fatal("special-source refusal changed fixture")
	}
}

// svnPropsetFromFile sets a property whose value must reach svn untouched.
//
// A bare "*" does not survive argv on Windows: TortoiseSVN's svn.exe is linked
// with CRT wildcard expansion, so the value was expanded into the directory
// listing and the property landed on every sibling - and on .svn, which is
// what made the command fail. Go does not quote a lone "*", and the expansion
// happens inside the receiving process, so there is nothing to quote on this
// side. A file takes argv out of the question entirely.
func (f fixture) svnPropsetFromFile(t *testing.T, name, value, target string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "propvalue")
	if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	f.svnRun(t, "propset", name, "-F", file, target)
}

// unicodeDestination is the non-ASCII destination this test moves into.
//
// It carries a CJK character everywhere except Windows, and the exception is
// the instrument, not the product. The fixture drives the external Subversion
// CLI, whose argv on Windows goes through the system ANSI codepage; CP1250
// holds the Polish letters and has no room for 新, which arrives as "?".
// The path would then be unaddressable for propget, copy and delete alike -
// measured in reports/NATIVE_SVN_WINDOWS_2026-09-08.md, where the same report
// records filees-svn.exe handling CJK-新.txt correctly while the CLI refuses it.
// Removing that limit is the reason the native helper exists.
//
// The Windows name keeps a space, a subdirectory and Polish diacritics, so the
// case stays a non-ASCII one here rather than quietly degrading to ASCII.
func unicodeDestination() string {
	if runtime.GOOS == "windows" {
		return "folder/Zażółć gęślą.txt"
	}
	return "folder/Zażółć gęślą 新.txt"
}
