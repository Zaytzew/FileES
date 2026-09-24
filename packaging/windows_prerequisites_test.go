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
