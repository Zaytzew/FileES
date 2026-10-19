//go:build windows && !nocfapi

package main

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestConversionWindowsBuffersAreAlignedAndZeroed(t *testing.T) {
	for _, size := range []int{1, 20, 42, 4096} {
		raw := conversionAlignedBuffer(size)
		if len(raw) != size || uintptr(unsafe.Pointer(&raw[0]))%8 != 0 {
			t.Fatal("unaligned Windows buffer", size)
		}
		for _, b := range raw {
			if b != 0 {
				t.Fatal("unterminated variable Windows record")
			}
		}
	}
}

func TestConversionRenameCannotOverwriteAnExistingObject(t *testing.T) {
	base := t.TempDir()
	source, target := filepath.Join(base, "source.txt"), filepath.Join(base, "target.txt")
	for name, text := range map[string]string{source: "source bytes", target: "target bytes"} {
		if err := os.WriteFile(name, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file, _, err := conversionOpen(source, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ)
	if err != nil {
		t.Fatal(err)
	}
	err = conversionRename(file, target)
	file.Close()
	if err == nil {
		t.Fatal("existing destination overwritten")
	}
	for name, text := range map[string]string{source: "source bytes", target: "target bytes"} {
		raw, err := os.ReadFile(name)
		if err != nil || string(raw) != text {
			t.Fatal("collision lost bytes", name, err)
		}
	}
}

func TestConversionDACLComparisonDoesNotRelaxPermissions(t *testing.T) {
	original := "D:AI(A;;FA;;;SY)(A;;FR;;;BU)"
	for _, row := range []struct {
		actual string
		same   bool
	}{
		{"D:(A;;FA;;;SY)(A;;FR;;;BU)", true},
		{"D:ARAI(A;;FA;;;SY)(A;;FR;;;BU)", true},
		{"D:P(A;;FA;;;SY)(A;;FR;;;BU)", false},
		{"D:(A;;FA;;;SY)(A;;FA;;;BU)", false},
		{"D:(A;;FR;;;BU)(A;;FA;;;SY)", false},
	} {
		if conversionSameDACL(original, row.actual) != row.same {
			t.Fatal("unsafe DACL comparison", row.actual)
		}
	}
}

func TestConversionDACLComparisonExcludesUnrequestedDescriptorComponents(t *testing.T) {
	want := "O:SYG:BAD:AI(A;;FA;;;SY)(A;;FR;;;BU)"
	for _, row := range []struct {
		actual string
		same   bool
	}{
		{"D:(A;;FA;;;SY)(A;;FR;;;BU)", true},
		{"O:BAG:SYD:(A;;FA;;;SY)(A;;FR;;;BU)", true},
		{"O:BAG:SYD:(A;;FA;;;SY)(A;;FR;;;BU)S:(AU;SA;FA;;;WD)", true},
		{"D:P(A;;FA;;;SY)(A;;FR;;;BU)", false},
		{"O:SYG:BAD:(A;;FA;;;SY)(A;;FA;;;BU)", false},
		{"D:(A;;FR;;;BU)(A;;FA;;;SY)", false},
		{"", false}, {"O:SYG:BA", false}, {"not a descriptor", false},
	} {
		if conversionSameDACL(want, row.actual) != row.same {
			t.Fatal("incorrect DACL-only comparison", row.actual)
		}
	}
}

func TestConversionMetadataDoesNotCopyDescriptorOwnerAndGroup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new-root")
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	file, info, err := conversionOpen(p, windows.GENERIC_READ|windows.WRITE_DAC|windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	before, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	oldPlanDACL := "O:SYG:BAD:P(A;OICI;FA;;;" + user.User.Sid.String() + ")"
	node := conversionObject{Directory: true, DACL: oldPlanDACL, Attributes: info.FileAttributes, Created: info.CreationTime, Modified: info.LastWriteTime}
	if err := conversionSetMetadata(file, node); err != nil {
		t.Fatal(err)
	}
	after, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION)
	if err != nil || before.String() != after.String() {
		t.Fatal("owner/group changed", err)
	}
	got, err := conversionDACL(file)
	if err != nil || !conversionSameDACL(got, oldPlanDACL) || got != "D:P(A;OICI;FA;;;"+user.User.Sid.String()+")" {
		t.Fatal("DACL-only proof changed permissions", got, err)
	}
}

func TestConversionSnapshotRefusesHardlinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "wc")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	if pin, err := conversionPinTree(t.Context(), root); err == nil {
		pin.close()
		t.Fatal("hardlink accepted")
	}
	raw, err := os.ReadFile(file)
	if err != nil || string(raw) != "keep" {
		t.Fatal("refusal changed source", err)
	}
}
