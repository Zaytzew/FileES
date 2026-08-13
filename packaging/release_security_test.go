package packaging

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseSigningToolKeepsPrivateKeyOffBuildAndVerifiesBeforeCommit(t *testing.T) {
	raw, err := os.ReadFile("../tools/release-sign-and-publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		`SIGNIFY_SEC_KEY="${SIGNIFY_SEC_KEY:-$HOME/.signify/filees-release.sec}"`,
		`RELEASE_ID="${RELEASE_ID:-}"`,
		`[ -z "$(svn status)" ]`,
		`svn update --quiet`,
		`channel-${CHANNEL}.v2.json`,
		`channel-${CHANNEL}.json`,
		`"$release_root"/*/*/manifest.json`,
		`"$SIGNIFY_BIN" -S`,
		`"$SIGNIFY_BIN" -V -q`,
		`cp "$candidate" "$channel_path"`,
		`status=$(svn status "$path" | cut -c1)`,
		`svn commit $commit_paths`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("release signing tool missing %q", required)
		}
	}
	if strings.Index(script, `cp "$candidate" "$channel_path"`) < strings.Index(script, `sign_manifest "$manifest_path"`) {
		t.Fatal("channel is promoted before immutable manifests are signed")
	}
	for _, forbidden := range []string{"release.sec\n", "cp \"$SIGNIFY_SEC_KEY\"", "svn add \"$SIGNIFY_SEC_KEY\""} {
		if strings.Contains(script, forbidden) {
			t.Errorf("release signing tool contains forbidden private-key handling %q", forbidden)
		}
	}
}

func TestServerReleasePreparationHasNoPrivateKeyAndDoesNotMoveChannel(t *testing.T) {
	raw, err := os.ReadFile("../tools/prepare-server-release.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"FILEES_RELEASE_PUBKEY", `svn status -q`, `svn update --quiet`,
		`source_revision=$(svn info --show-item revision | tr -d '\r\n')`,
		`SVN returned an invalid source revision`,
		`openbsd-binary-policy.json`, `channel-stable.json`,
		`review, then svn add/commit only releases/$RELEASE_ID`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("server release preparation missing %q", required)
		}
	}
	for _, forbidden := range []string{"SIGNIFY_SEC", "release.sec", "channels/stable.json"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("build-host release preparation contains forbidden %q", forbidden)
		}
	}
}

func TestServerBuildInjectsOnlyPublicReleaseTrust(t *testing.T) {
	raw, err := os.ReadFile("build-server.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"FILEES_RELEASE_PUBKEY", "injectedServerReleasePublicKeyB64", "base64",
		"refusing placeholder release public key",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("server build missing release trust control %q", required)
		}
	}
	for _, forbidden := range []string{"RELEASE_SEC", "release.sec", "SIGNIFY_SEC_KEY"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("server build references private signing material %q", forbidden)
		}
	}
}

func TestServerBuildRejectsWrongTargetArtifacts(t *testing.T) {
	raw, err := os.ReadFile("build-server.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		`reported_goos=$(CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go env GOOS)`,
		`reported_goarch=$(CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go env GOARCH)`,
		`[ "$magic" = "7f454c46" ]`,
		`go version -m "$binary"`,
		`"GOOS=$goos"`,
		`"GOARCH=$goarch"`,
		`verify_binary_target "$tmp/bin/$command" "$command"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("server build missing target guard %q", required)
		}
	}
}

func TestClientBuildInjectsOnlyPublicReleaseTrust(t *testing.T) {
	raw, err := os.ReadFile("build-gui.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"FILEES_RELEASE_PUBKEY", "FILEES_RELEASE_KEY_ID", "injectedClientReleasePublicKeyB64",
		"injectedClientReleaseKeyID", "base64", "refusing placeholder release public key",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("client build missing release trust control %q", required)
		}
	}
	for _, forbidden := range []string{"RELEASE_SEC", "release.sec", "SIGNIFY_SEC_KEY"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("client build references private signing material %q", forbidden)
		}
	}
}
