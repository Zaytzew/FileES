//go:build native_svn_probe && windows

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filees/internal/svnurl"
)

func TestNativeCommitThousandTargetsAtomic(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	run := func(tool string, args ...string) string {
		t.Helper()
		out, err := exec.Command(nativeFixtureTool(t, tool), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return string(out)
	}
	run("svnadmin", "create", repo)
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(root, "absent-cli.exe")}).(*execClient)
	if _, err := c.Checkout(t.Context(), svnurl.File(repo), wc); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(wc, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 1200)
	for i := range paths {
		paths[i] = fmt.Sprintf("%04d_新_%s.txt", i, strings.Repeat("segment", 5))
		if err := os.WriteFile(filepath.Join(wc, paths[i]), []byte("synthetic\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if len(strings.Join(paths, " ")) < 32768 {
		t.Fatal("fixture below Windows command-line boundary")
	}
	if _, err := c.Add(t.Context(), wc, paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "unselected.txt"), []byte("never publish"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(t.Context(), wc, []string{"unselected.txt"}); err != nil {
		t.Fatal(err)
	}
	bad := [][]byte{nil, []byte(paths[0]), []byte(paths[0] + "\x00\x00"), []byte(paths[0] + "\x00../escape\x00"), []byte(paths[0] + "\x00" + paths[0] + "\x00"), {0xff, 0}, {0xc0, 0xaf, 0}, {0xed, 0xa0, 0x80, 0}, []byte("a\x1ab\x00"), bytes.Repeat([]byte{'a'}, nativeCommitTargetBytes+1)}
	for _, input := range bad {
		cmd := exec.Command(helper, "commit", "--wc", wc, "-m", "must refuse", "--targets-stdin")
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		var receipt struct{ OK bool }
		if err == nil || json.Unmarshal(out, &receipt) != nil || receipt.OK {
			t.Fatalf("bad input accepted len=%d: %s %v", len(input), out, err)
		}
		if strings.TrimSpace(run("svnlook", "youngest", repo)) != "0" {
			t.Fatal("refusal changed HEAD")
		}
	}
	_, rev, err := c.CommitWithRevision(t.Context(), wc, svnurl.File(repo), paths, "one atomic transaction", false)
	if err != nil || rev != 1 {
		t.Fatal(rev, err)
	}
	changed := run("svnlook", "changed", "-r", "1", repo)
	if strings.Count(changed, "\n") != len(paths) || strings.Contains(changed, "unselected") {
		t.Fatal("transaction widened or split")
	}
	if strings.TrimSpace(run("svnlook", "youngest", repo)) != "1" {
		t.Fatal("more than one revision")
	}
}
