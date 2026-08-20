//go:build !windows

package repoworker

import (
	"os"
	"strings"
	"testing"
)

func TestVerifyServiceWorkingCopyOwnerForUID(t *testing.T) {
	root := t.TempDir()
	if err := verifyServiceWorkingCopyOwnerForUID(root, os.Geteuid()); err != nil {
		t.Fatalf("owner should be accepted: %v", err)
	}

	err := verifyServiceWorkingCopyOwnerForUID(root, os.Geteuid()+1)
	if err == nil || !strings.Contains(err.Error(), "run FileES administrative commands as the service-state owner") {
		t.Fatalf("wrong effective uid error = %v", err)
	}
}
