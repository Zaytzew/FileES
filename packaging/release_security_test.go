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
		`[ -z "$(svn status)" ]`,
		`svn update --quiet`,
		`channels/${CHANNEL}.v2.json`,
		`"$release_root"/*/*/manifest.json`,
		`"$SIGNIFY_BIN" -S`,
		`"$SIGNIFY_BIN" -V -q`,
		`mv "$tmp_sig" "${path}.sig"`,
		`svn commit $signed_paths`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("release signing tool missing %q", required)
		}
	}
	for _, forbidden := range []string{"release.sec\n", "cp \"$SIGNIFY_SEC_KEY\"", "svn add \"$SIGNIFY_SEC_KEY\""} {
		if strings.Contains(script, forbidden) {
			t.Errorf("release signing tool contains forbidden private-key handling %q", forbidden)
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
