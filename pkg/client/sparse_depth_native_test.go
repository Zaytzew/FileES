//go:build native_svn_probe && (windows || linux)

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The same scenario through the native helper with no svn CLI at all: release
// builds route working-copy operations there, so choosing paths must not
// depend on a CLI the user may not have.
func TestNativeUpdateSetDepthChoosesPathsInASparseCopy(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	run := func(tool string, args ...string) {
		t.Helper()
		if out, err := exec.Command(nativeFixtureTool(t, tool), args...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	c := New(Options{NativeSVNPath: helper, SvnPath: filepath.Join(t.TempDir(), "absent-cli")}).(*execClient)
	sparseDepthScenario(t, c, run)
}
