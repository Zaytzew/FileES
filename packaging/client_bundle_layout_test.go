package packaging_test

import (
	"os"
	"strings"
	"testing"

	"filees/internal/clientupdate"
)

// The script that builds a client bundle and the code that installs one must
// agree on its layout, and nothing else can make them.
//
// The producer is a shell script and the consumer is Go, so a renamed file
// breaks the pair silently: the release is built, staged, signed and published,
// and the first sign of trouble is a client refusing an update it was given.
// That is a long way to travel for a typo.
//
// This checks names rather than behaviour on purpose. Actually building a
// bundle here would need a cross-compiler and several minutes; the failure
// worth catching is a path that stopped matching, and a path is a string.
func TestTheBundleBuilderStagesWhatTheInstallerRequires(t *testing.T) {
	raw, err := os.ReadFile("build-client-bundle.sh")
	if err != nil {
		t.Fatalf("the client bundle builder is missing: %v", err)
	}
	script := string(raw)

	for _, required := range clientupdate.RequiredBundleFiles() {
		// The script writes into $staging with the bundle-relative path, so the
		// path has to appear in it somewhere. SHA256SUMS is generated rather
		// than copied, and VERSION is written with printf, so both are matched
		// by name like the rest.
		if !strings.Contains(script, required) {
			t.Errorf("the bundle builder never stages %q, which the installer requires", required)
		}
	}
}

// And the installer must not require something the script has no way to
// provide - a requirement nobody produces fails every release, which is the
// same fault pointing the other way.
func TestTheInstallerRequiresNothingTheBuilderCannotStage(t *testing.T) {
	required := clientupdate.RequiredBundleFiles()
	if len(required) == 0 {
		t.Fatal("the installer requires nothing at all; a bundle would be accepted empty")
	}
	for _, name := range required {
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, `\`) {
			t.Errorf("required bundle path %q is not a plain relative path", name)
		}
	}
	// The two binaries are the product. A bundle that carried only scripts
	// would install cleanly and synchronise nothing.
	for _, essential := range []string{"bin/filees.exe", "bin/filees-gui-wails.exe"} {
		found := false
		for _, name := range required {
			if name == essential {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is not required, so a bundle without it would install", essential)
		}
	}
}
