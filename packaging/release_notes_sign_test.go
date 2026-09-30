package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// notes.json is signed with the manifests, in the same commit, and a beta
// promotion of an alpha release needs its signature like a manifest's.
func TestReleaseNotesAreSignedWithTheRelease(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	script, err := filepath.Abs("../tools/release-sign-and-publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, channel, manifestSig, notesSig string
		draft, fail                          bool
	}{
		{name: "alpha signs notes", channel: "alpha"},
		{name: "leftover draft", channel: "alpha", draft: true, fail: true},
		{name: "beta needs signed notes", channel: "beta", manifestSig: "valid", fail: true},
		{name: "beta with signed notes", channel: "beta", manifestSig: "valid", notesSig: "valid"},
		{name: "beta with bad notes signature", channel: "beta", manifestSig: "valid", notesSig: "forged", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, text string) {
				t.Helper()
				path = filepath.Join(root, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(root, ".svn"), 0o700); err != nil {
				t.Fatal(err)
			}
			write("test.sec", "x")
			write("test.pub", "x")
			write("releases/test-release/channel.json", "{\n  \"release_id\": \"test-release\"\n}\n")
			write("releases/test-release/openbsd-amd64/manifest.json", "manifest")
			if tc.manifestSig != "" {
				write("releases/test-release/openbsd-amd64/manifest.json.sig", tc.manifestSig)
			}
			write("releases/test-release/notes.json", "notes")
			if tc.notesSig != "" {
				write("releases/test-release/notes.json.sig", tc.notesSig)
			}
			if tc.draft {
				write("releases/test-release/notes.draft.json", "draft")
			}
			commands := `
FILEES_BIN_WC=$(cd "$FILEES_BIN_WC" && pwd)
export FILEES_BIN_WC
mkdir -p "$FILEES_BIN_WC/test-bin"
cat >"$FILEES_BIN_WC/test-bin/svn" <<'STUB'
#!/bin/sh
case "$1" in
  status|update|cleanup|add|delete) exit 0 ;;
  commit) printf '%s\n' "$*" >>"$FILEES_BIN_WC/commits.log" ;;
  *) exit 1 ;;
esac
STUB
chmod +x "$FILEES_BIN_WC/test-bin/svn"
PATH="$FILEES_BIN_WC/test-bin:$PATH"
export PATH
signify_stub() {
  operation=$1
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in -x) signature=$2; shift 2 ;; -q) shift ;; *) shift 2 ;; esac
  done
  case "$operation" in
    -V) [ "$(cat "$signature" 2>/dev/null)" = valid ] ;;
    -S) printf valid >"$signature" ;;
    *) return 1 ;;
  esac
}
. "$1"
`
			cmd := exec.Command(shell, "-c", commands, script, script)
			cmd.Env = append(os.Environ(), "FILEES_BIN_WC="+root, "SIGNIFY_BIN=signify_stub",
				"SIGNIFY_SEC_KEY="+filepath.Join(root, "test.sec"), "SIGNIFY_PUB_KEY="+filepath.Join(root, "test.pub"),
				"RELEASE_ID=test-release", "CHANNEL="+tc.channel)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v output=%s", err, out)
			}
			if tc.fail {
				if _, err := os.Stat(filepath.Join(root, "commits.log")); err == nil {
					t.Fatal("a refused run committed")
				}
				return
			}
			sig, err := os.ReadFile(filepath.Join(root, "releases/test-release/notes.json.sig"))
			if err != nil || string(sig) != "valid" {
				t.Fatalf("notes signature %q %v", sig, err)
			}
			commits, err := os.ReadFile(filepath.Join(root, "commits.log"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.notesSig == "" && !strings.Contains(string(commits), "releases/test-release/notes.json.sig") {
				t.Fatalf("notes signature is not in the promotion commit: %s", commits)
			}
		})
	}
}
