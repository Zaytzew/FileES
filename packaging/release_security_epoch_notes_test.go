package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A release that raises security_epoch is signed only with a security item
// in its notes (owner, 2026-09-29).
func TestRaisedSecurityEpochNeedsSecurityNotes(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	script, err := filepath.Abs("../tools/release-sign-and-publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	const security = `{"items":[{"sequence":2,"scope":"server","kind": "security","pl":"Poprawki bezpieczeństwa","en":"Security fixes"}]}`
	const feature = `{"items":[{"sequence":2,"scope":"server","pl":"Nowość","en":"Feature"}]}`
	for _, tc := range []struct {
		name, notes string
		oldEpoch    string
		fail        bool
	}{
		{name: "raised with security item", notes: security, oldEpoch: "1"},
		{name: "raised without notes", oldEpoch: "1", fail: true},
		{name: "raised without security item", notes: feature, oldEpoch: "1", fail: true},
		{name: "unchanged without notes", oldEpoch: "2"},
		{name: "first release on the channel", oldEpoch: ""},
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
			write("releases/test-release/channel.json", "{\n  \"release_id\": \"test-release\",\n  \"sequence\": 2,\n  \"security_epoch\": 2\n}\n")
			write("releases/test-release/openbsd-amd64/manifest.json", "manifest")
			if tc.notes != "" {
				write("releases/test-release/notes.json", tc.notes)
			}
			if tc.oldEpoch != "" {
				write("channels/alpha.json", "{\n  \"release_id\": \"old\",\n  \"sequence\": 1,\n  \"security_epoch\": "+tc.oldEpoch+"\n}\n")
				write("channels/alpha.json.sig", "valid")
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
				"RELEASE_ID=test-release", "CHANNEL=alpha")
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v output=%s", err, out)
			}
			if _, statErr := os.Stat(filepath.Join(root, "commits.log")); tc.fail && statErr == nil {
				t.Fatal("a refused run committed")
			}
		})
	}
}
