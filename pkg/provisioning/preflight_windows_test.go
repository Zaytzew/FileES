package provisioning

import (
	"errors"
	"filees/pkg/errcat"
	"testing"
)

// These roots include deliberately nonexistent drives/shares. Their rejection
// must happen before canonicalization or a network/filesystem read.
func TestWindowsPreflightRejectsDriveAndShareRootsBeforeIO(t *testing.T) {
	for _, root := range []string{`C:\`, `D:/`, `Z:\`, `C:\folder\..`, `\\?\C:\`, `\\filees-preflight.invalid\share`, `\\filees-preflight.invalid\share\`, `\\?\UNC\filees-preflight.invalid\share\`} {
		for _, mode := range []LocalPathMode{LocalPathCreate, LocalPathCreateResume, LocalPathAttach, LocalPathAttachResume} {
			t.Run(string(mode)+root, func(t *testing.T) {
				_, err := PreflightLocalPath(root, mode, nil)
				var fault errcat.Fault
				if !errors.As(err, &fault) || fault.Key != "repo.local_volume_root" {
					t.Fatalf("root was not rejected before IO: %v", err)
				}
			})
		}
	}
}
