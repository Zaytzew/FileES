//go:build openbsd

package platform

import (
	"os"
	"testing"
)

// This is deliberately a native OpenBSD system test. It must be run as root
// on a real test host; cross-compilation is not acceptance for filesystem
// ownership and mode behavior.
func TestApplyStateDirsRestoresSharedRuntimeContract(t *testing.T) {
	if os.Getenv("FILEES_OPENBSD_SYSTEM_TEST") != "1" {
		t.Skip("set FILEES_OPENBSD_SYSTEM_TEST=1 on a real OpenBSD test host")
	}
	if os.Geteuid() != 0 {
		t.Fatal("native state-directory test requires root")
	}

	b := &openbsdBackend{}
	if err := b.ApplyStateDirs("_filees-state"); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		path         string
		owner, group string
		mode         os.FileMode
	}{
		{"/etc/filees", "_filees-state", "_filees-public", 0o750},
		{"/var/run/filees", "_filees-state", "_filees-public", 0o750},
		{"/var/www/run/filees", "_filees-links", "www", 0o750},
	}
	manager := SystemOwnership{}
	for _, tc := range want {
		t.Run(tc.path, func(t *testing.T) {
			info, err := os.Lstat(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.mode {
				t.Fatalf("mode=%#o want=%#o", got, tc.mode)
			}
			got, err := manager.Stat(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			wantOwner, err := manager.Resolve(tc.owner, tc.group)
			if err != nil {
				t.Fatal(err)
			}
			if got != wantOwner {
				t.Fatalf("ownership=%+v want=%+v", got, wantOwner)
			}
		})
	}
}
