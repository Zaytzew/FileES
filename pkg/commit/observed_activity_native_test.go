//go:build native_svn_probe && windows

package commit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
)

func TestNativeIncomingUpdateNeverEntersOutgoingQueue(t *testing.T) {
	helper := os.Getenv("FILEES_SVN_PROBE")
	if helper == "" {
		t.Fatal("FILEES_SVN_PROBE required")
	}
	testIncomingUpdateNeverEntersOutgoingQueue(t, client.New(client.Options{NativeSVNPath: helper, SvnPath: filepath.Join(t.TempDir(), "absent-cli.exe"), Timeout: 10 * time.Second}))
}
