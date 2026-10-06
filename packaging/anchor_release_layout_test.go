package packaging_test

import (
	"os"
	"strings"
	"testing"
)

func TestAlphaRequiresMatchingAnchorReleaseSetBeforeBuildingDaemon(t *testing.T) {
	raw, err := os.ReadFile("build-client-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	gate := strings.Index(script, `set -- "$@" --anchor-release "$FILEES_CFAPI_HELPER"`)
	pack := strings.Index(script, `go run ./cmd/filees-native-package "$@"`)
	build := strings.Index(script, `-o "$out/bin/$daemon" ./cmd/filees`)
	if gate < 0 || pack <= gate || build <= pack {
		t.Fatal("alpha does not validate its exact helper/runtime pair before daemon build")
	}
	if !strings.Contains(script[:gate], `"${FILEES_RELEASE_CHANNEL:-}" = alpha ]; then`) {
		t.Fatal("anchor release runtime requirement must not apply to beta/Store/Linux")
	}
}
