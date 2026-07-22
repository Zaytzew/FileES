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
