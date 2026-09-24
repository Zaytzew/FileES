package packaging_test

import (
	"os"
	"strings"
	"testing"
)

// The owner's decision of 2026-09-24, after a clean Windows showed what the
// owner's machine hid. It lives in a build input no Go code executes, so only
// a test keeps it from quietly disappearing.

// The Microsoft Store package ships without Explorer anchors and without any
// Cloud Files API code: the daemon is built with nocfapi, and the script
// refuses a binary that still carries it.
func TestStorePackageIsBuiltWithoutCloudFiles(t *testing.T) {
	raw, err := os.ReadFile("windows/build-store-msix.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	if !strings.Contains(script, "-tags native_svn_bundle,nocfapi") {
		t.Fatal("the Store daemon is not built with the nocfapi tag")
	}
	for _, marker := range []string{"'cldapi'", "'CfGetPlaceholderState'", "'filees-cfapi.exe'"} {
		if !strings.Contains(script, marker) {
			t.Fatalf("the Store build no longer checks the daemon for %s", marker)
		}
	}
}

// Beta and stable desktop bundles are the nocfapi build of the revision; alpha
// keeps every feature. The channel decides, and a beta daemon that still holds
// Cloud Files code stops the build (owner's decision, 2026-09-24).
func TestBetaBundleIsBuiltWithoutCloudFiles(t *testing.T) {
	raw, err := os.ReadFile("build-client-bundle.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		`beta|stable) daemon_tags="$daemon_tags,nocfapi" ;;`,
		`-tags "$daemon_tags"`,
		`grep -a -i -q -e cldapi -e CfGetPlaceholderState -e filees-cfapi.exe`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("build-client-bundle.sh lacks %q", required)
		}
	}
	if strings.Contains(script, "-tags native_svn_bundle ") {
		t.Fatal("a daemon build bypasses the channel's tags")
	}
}
