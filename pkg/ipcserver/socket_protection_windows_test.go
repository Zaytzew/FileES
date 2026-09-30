//go:build windows

package ipcserver

import (
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/privatefile"
	"golang.org/x/sys/windows"
)

func TestSocketRefusesSharedDirectoryWithoutChangingACL(t *testing.T) {
	path := protectedTestSocket(t)
	dir := filepath.Dir(path)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	defer privatefile.Harden(dir)
	if listener, _, err := listenProtectedSocket(path, protectSocket); err == nil {
		listener.Close()
		t.Fatal("shared IPC directory accepted")
	}
	if err := privatefile.Verify(dir); err == nil {
		t.Fatal("shared directory ACL was silently changed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("listener created before directory check: %v", err)
	}
}
