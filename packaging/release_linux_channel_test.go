package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The Linux server has its own channels (owner, 2026-10-02). A Linux release
// must never move the OpenBSD server's channel and the other way round, and
// the maturity rule (first signature on alpha) holds on the Linux track too.
func TestLinuxServerReleasesHaveTheirOwnChannels(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	script, err := filepath.Abs("../tools/release-sign-and-publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	const stubs = `
FILEES_BIN_WC=$(cd "$FILEES_BIN_WC" && pwd)
export FILEES_BIN_WC
mkdir -p "$FILEES_BIN_WC/test-bin"
cat >"$FILEES_BIN_WC/test-bin/svn" <<'STUB'
#!/bin/sh
  case "$1" in
    status|update|cleanup|commit) exit 0 ;;
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
	const payload = "{\n  \"release_id\": \"r9-server-linux\"\n}\n"
	for _, tc := range []struct {
		name, candidate, platform, channel, signature string
		moved                                         string // channel file that must change; "" = refusal
	}{
		{"linux release to alpha-linux", "channel-linux.json", "linux-amd64", "alpha-linux", "", "alpha-linux"},
		{"linux release refused on alpha", "channel-linux.json", "linux-amd64", "alpha", "", ""},
		{"openbsd release refused on alpha-linux", "channel.json", "openbsd-amd64", "alpha-linux", "", ""},
		{"openbsd release still goes to alpha", "channel.json", "openbsd-amd64", "alpha", "", "alpha"},
		{"unsigned linux release refused on beta-linux", "channel-linux.json", "linux-amd64", "beta-linux", "", ""},
		{"signed linux release promoted to beta-linux", "channel-linux.json", "linux-amd64", "beta-linux", "valid", "beta-linux"},
		{"unknown linux stage", "channel-linux.json", "linux-amd64", "gamma-linux", "", ""},
		{"CLI first signature has its own track", "channel-cli.json", "linux-amd64", "cli", "", "cli"},
		{"CLI refused on desktop alpha", "channel-cli.json", "linux-amd64", "alpha", "", ""},
		{"CLI refused on server alpha-linux", "channel-cli.json", "linux-amd64", "alpha-linux", "", ""},
		{"desktop refused on CLI", "channel.v2.json", "linux-amd64", "cli", "", ""},
		{"server refused on CLI", "channel-linux.json", "linux-amd64", "cli", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			write(".svn/entries", "")
			write("test.sec", "not a private key")
			write("test.pub", "not a public key")
			write("releases/r9-server-linux/"+tc.candidate, payload)
			manifest := "releases/r9-server-linux/" + tc.platform + "/manifest.json"
			if tc.candidate == "channel-cli.json" {
				manifest = "releases/r9-server-linux/cli/" + tc.platform + "/manifest.json"
				write("releases/r9-server-linux/built-for-channel", "cli\n")
				write(manifest, `{ "component": "cli" }`)
			} else {
				write(manifest, "immutable manifest")
			}
			if tc.signature != "" {
				write(manifest+".sig", tc.signature)
			}
			channels := []string{"alpha", "beta", "alpha-linux", "beta-linux", "cli", "android"}
			for _, channel := range channels {
				write("channels/"+channel+".json", "old "+channel)
				write("channels/"+channel+".json.sig", "old signature")
			}
			cmd := exec.Command(shell, "-c", stubs, script, script)
			cmd.Env = append(os.Environ(), "FILEES_BIN_WC="+root, "SIGNIFY_BIN=signify_stub",
				"SIGNIFY_SEC_KEY="+filepath.Join(root, "test.sec"), "SIGNIFY_PUB_KEY="+filepath.Join(root, "test.pub"),
				"RELEASE_ID=r9-server-linux", "CHANNEL="+tc.channel)
			out, err := cmd.CombinedOutput()
			if (err != nil) != (tc.moved == "") {
				t.Fatalf("err=%v output=%s", err, out)
			}
			for _, channel := range channels {
				raw, err := os.ReadFile(filepath.Join(root, "channels", channel+".json"))
				if err != nil {
					t.Fatal(err)
				}
				want := "old " + channel
				if channel == tc.moved {
					want = payload
				}
				if string(raw) != want {
					t.Errorf("channel %s = %q, want %q\n%s", channel, raw, want, out)
				}
			}
			if tc.moved == "" && tc.signature == "" {
				if _, err := os.Stat(filepath.Join(root, manifest+".sig")); !os.IsNotExist(err) {
					t.Error("a refused promotion signed the manifest")
				}
			}
		})
	}
}
