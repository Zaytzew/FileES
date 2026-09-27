//go:build !windows

package packaging

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real SVN exercises publication followed by retention, including an old
// channel target and recovery of deleted releases from repository history.
func TestPublisherRetainsFivePerKind(t *testing.T) {
	for _, tool := range []string{"sh", "svn", "svnadmin"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	root := t.TempDir()
	repo, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc")
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		return string(out)
	}
	run("svnadmin", "create", repo)
	url := "file://" + filepath.ToSlash(repo)
	run("svn", "checkout", "-q", url, wc)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"", "-server", "-beta", "-android"} {
		for rev := 1; rev <= 8; rev++ {
			id := fmt.Sprintf("r%d%s", rev, kind)
			write(filepath.Join(wc, "releases", id, "payload"), id)
		}
	}
	write(filepath.Join(wc, "channels", "stable.json"), "{\n  \"release_id\": \"r1-server\"\n}\n")
	write(filepath.Join(wc, "channels", "alpha.json"), "{\n  \"release_id\": \"r2-server\"\n}\n")
	candidate := "{\n  \"release_id\": \"r8-server\"\n}\n"
	write(filepath.Join(wc, "releases/r8-server/channel.json"), candidate)
	write(filepath.Join(wc, "releases/r8-server/openbsd-amd64/manifest.json"), "test manifest")
	run("svn", "add", "-q", filepath.Join(wc, "releases"), filepath.Join(wc, "channels"))
	run("svn", "commit", "-q", "-m", "fixture", wc)
	// A fake signer tests control flow only; no private release keys are used.
	signer := filepath.Join(root, "signify")
	write(signer, `#!/bin/sh
op=$1
shift
while [ "$#" -gt 0 ]; do
 case "$1" in -x) sig=$2; shift 2 ;; -q) shift ;; *) shift 2 ;; esac
done
case "$op" in
 -S) [ "${FAIL_SIGN:-0}" != 1 ] || exit 1; printf valid >"$sig" ;;
 -V) [ "$(cat "$sig" 2>/dev/null)" = valid ] ;;
 *) exit 1 ;;
esac
`)
	key := filepath.Join(root, "fake-key")
	write(key, "test only")
	script, err := filepath.Abs("../tools/release-sign-and-publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	publish := func(fail bool) {
		t.Helper()
		cmd := exec.Command("sh", script)
		cmd.Env = append(os.Environ(), "FILEES_BIN_WC="+wc, "SIGNIFY_BIN="+signer, "SIGNIFY_SEC_KEY="+key, "SIGNIFY_PUB_KEY="+key, "RELEASE_ID=r8-server", "CHANNEL=alpha", "KEEP=99")
		if fail {
			cmd.Env = append(cmd.Env, "FAIL_SIGN=1")
		}
		out, e := cmd.CombinedOutput()
		if (e != nil) != fail {
			t.Fatalf("publish: %v\n%s", e, out)
		}
	}
	publish(true)
	if _, err := os.Stat(filepath.Join(wc, "releases/r2-server")); err != nil {
		t.Fatal("failed signing pruned history", err)
	}
	publish(false)
	for _, kind := range []string{"", "-server", "-beta", "-android"} {
		for rev := 1; rev <= 8; rev++ {
			id := fmt.Sprintf("r%d%s", rev, kind)
			_, err := os.Stat(filepath.Join(wc, "releases", id))
			want := rev >= 4 || id == "r1-server"
			if want && err != nil {
				t.Errorf("retained %s: %v", id, err)
			}
			if !want && !os.IsNotExist(err) {
				t.Errorf("old release %s remains: %v", id, err)
			}
		}
	}
	if got := run("svn", "status", wc); got != "" {
		t.Fatal("dirty WC", got)
	}
	if got := run("svn", "cat", url+"/releases/r2-server/payload@1"); got != "r2-server" {
		t.Fatal("history lost", got)
	}
	before := strings.TrimSpace(run("svn", "info", "--show-item", "revision", url))
	publish(false) // Already published: cleanup is safe and creates no revision.
	after := strings.TrimSpace(run("svn", "info", "--show-item", "revision", url))
	if before != after {
		t.Fatalf("idempotent publish made revision %s -> %s", before, after)
	}
}
