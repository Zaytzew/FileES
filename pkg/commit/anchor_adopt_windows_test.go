//go:build windows

package commit

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptPathRefusesRecallBeforeInvokingSVN(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_OFFLINE); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_NORMAL) })
	p := &anchorAdoptProbe{}
	s := &Service{Cli: p}
	s.BindWorkingCopy("fixture", root)
	if err := s.AdoptPath(t.Context(), root, "file", 31); err == nil {
		t.Fatal("recall accepted")
	}
	if p.calls != 0 {
		t.Fatal("SVN invoked for partial file")
	}
}
