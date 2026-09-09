//go:build native_svn_probe

package client

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Independent fixture/oracle only. Production clients deliberately get an
// absent SvnPath. Explicit paths let acceptance remove SVN/SDK from PATH.
func nativeFixtureTool(t *testing.T, name string) string {
	t.Helper()
	if name == "svn" || name == "svnadmin" || name == "svnlook" {
		if path := os.Getenv("FILEES_PROBE_" + strings.ToUpper(name)); path != "" {
			name = path
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
