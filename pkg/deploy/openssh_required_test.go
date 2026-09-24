package deploy

import (
	"errors"
	"testing"

	"filees/pkg/errcat"
)

// With no ssh on PATH the tunnel must not get as far as exec: the fault it
// returns is what lets the daemon tell the user to install the OpenSSH client
// instead of relaying "executable file not found in %PATH%".
func TestRequireOpenSSHNamesTheMissingClient(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := requireOpenSSH()
	var fault errcat.Fault
	if !errors.As(err, &fault) || fault.Key != errcat.KeyActivationNoOpenSSH || fault.Code != "ACTIVATION-1005" {
		t.Fatalf("requireOpenSSH without ssh = %v, want the ACTIVATION-1005 fault", err)
	}
}