//go:build windows

package deploy

import (
	"os"
	"testing"
)

// An MSI installation has no package identity, so OpenSSH keeps starting this
// very executable as askpass - the path that worked before the Store alias.
func TestUnpackagedAskpassIsThisExecutable(t *testing.T) {
	family, err := packageFamilyName()
	if err != nil {
		t.Fatal(err)
	}
	if family != "" {
		t.Skipf("test binary runs inside package %s", family)
	}
	got, err := askpassExecutable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("askpass = %q, want this executable %q", got, want)
	}
}