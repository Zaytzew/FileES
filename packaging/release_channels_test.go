package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real publisher's file/control flow with fake external commands.
// This does not test cryptography: signify verification has separate coverage.
func TestReleaseChannelPromotionIsolation(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		shell = filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, err := os.Stat(shell); err != nil {
			t.Skip("POSIX shell unavailable")
		}
	}
	script, err := filepath.Abs("../tools/release-sign-and-publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"server", "desktop"} {
		for _, tc := range []struct {
			channel, signature, builtFor string
			fail                         bool
		}{
			// Releases without built-for-channel: the old promotion rule.
			{"beta", "valid", "", false}, {"beta", "", "", true}, {"beta", "invalid", "", true},
			{"stable", "", "", true}, {"alpha", "", "", false},
			// Two builds from one revision (2026-09-24): a release goes only to
			// the channel it was built for, and may be signed there first.
			{"beta", "", "beta", false}, {"alpha", "", "alpha", false},
			{"beta", "valid", "alpha", true}, {"alpha", "", "beta", true}, {"stable", "", "beta", true},
		} {
			t.Run(schema+"/"+tc.channel+"/"+tc.signature+"/"+tc.builtFor, func(t *testing.T) {
				root := t.TempDir()
				write := func(path, text string) {
					t.Helper()
					path = filepath.Join(root, filepath.FromSlash(path))
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(filepath.Join(root, ".svn"), 0700); err != nil {
					t.Fatal(err)
				}
				write("test.sec", "not a private key")
				write("test.pub", "not a public key")
				candidate := "releases/test-release/channel.json"
				suffix := ".json"
				manifests := []string{"releases/test-release/openbsd-amd64/manifest.json"}
				if schema == "desktop" {
					candidate = "releases/test-release/channel.v2.json"
					suffix = ".v2.json"
					manifests = []string{"releases/test-release/desktop/windows-amd64/manifest.json", "releases/test-release/desktop/linux-amd64/manifest.json"}
				}
				payload := "{\n  \"release_id\": \"test-release\"\n}\n"
				write(candidate, payload)
				if tc.builtFor != "" {
					write("releases/test-release/built-for-channel", tc.builtFor+"\n")
				}
				for _, manifest := range manifests {
					write(manifest, "immutable manifest")
					if tc.signature != "" {
						write(manifest+".sig", tc.signature)
					}
				}
				for _, channel := range []string{"alpha", "beta", "stable"} {
					write("channels/"+channel+suffix, "old "+channel)
					write("channels/"+channel+suffix+".sig", "old "+channel+" signature")
				}
				commands := `
# Windows drive letters are not valid PATH entries in the POSIX shell.
# Normalize before installing the stub, including for the retention subprocess.
FILEES_BIN_WC=$(cd "$FILEES_BIN_WC" && pwd)
export FILEES_BIN_WC
mkdir -p "$FILEES_BIN_WC/test-bin"
cat >"$FILEES_BIN_WC/test-bin/svn" <<'STUB'
#!/bin/sh

  printf '%s\n' "$*" >>"$FILEES_BIN_WC/svn-calls.log"
  case "$1" in
    status|update|cleanup) exit 0 ;;
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
				cmd := exec.Command(shell, "-c", commands, filepath.ToSlash(script), filepath.ToSlash(script))
				cmd.Env = append(os.Environ(), "FILEES_BIN_WC="+filepath.ToSlash(root), "SIGNIFY_BIN=signify_stub",
					"SIGNIFY_SEC_KEY="+filepath.ToSlash(filepath.Join(root, "test.sec")), "SIGNIFY_PUB_KEY="+filepath.ToSlash(filepath.Join(root, "test.pub")), "RELEASE_ID=test-release", "CHANNEL="+tc.channel)
				out, err := cmd.CombinedOutput()
				if (err != nil) != tc.fail {
					t.Fatalf("err=%v output=%s", err, out)
				}
				read := func(path string) string {
					t.Helper()
					raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
					if err != nil {
						t.Fatal(err)
					}
					return string(raw)
				}
				for _, channel := range []string{"alpha", "beta", "stable"} {
					want, sig := "old "+channel, "old "+channel+" signature"
					if channel == tc.channel && !tc.fail {
						want, sig = payload, "valid"
					}
					if got := read("channels/" + channel + suffix); got != want {
						t.Errorf("%s changed: %q", channel, got)
					}
					if got := read("channels/" + channel + suffix + ".sig"); got != sig {
						t.Errorf("%s signature changed: %q", channel, got)
					}
				}
				for _, manifest := range manifests {
					if read(manifest) != "immutable manifest" {
						t.Fatal("manifest changed")
					}
					if tc.signature != "" && read(manifest+".sig") != tc.signature {
						t.Fatal("existing signature changed")
					}
					if tc.fail && tc.signature == "" {
						if _, err := os.Stat(filepath.Join(root, manifest+".sig")); !os.IsNotExist(err) {
							t.Fatal("promotion signed a missing manifest")
						}
					}
				}
				if tc.fail {
					if _, err := os.Stat(filepath.Join(root, "commits.log")); !os.IsNotExist(err) {
						t.Fatal("failed promotion committed")
					}
				} else {
					// Publication invokes retention in a child shell. It must use
					// the same fake SVN, not escape into the host installation.
					if !strings.Contains(read("svn-calls.log"), "cleanup --vacuum-pristines") {
						t.Fatal("retention did not finish through the isolated SVN stub")
					}
					commit := read("commits.log")
					if !strings.Contains(commit, "channels/"+tc.channel+suffix) {
						t.Fatal("channel absent from commit")
					}
					if tc.channel == "beta" && tc.signature != "" && strings.Contains(commit, "releases/") {
						t.Fatal("promotion touched immutable release")
					}
				}
			})
		}
	}
}
