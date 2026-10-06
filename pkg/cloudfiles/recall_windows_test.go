//go:build windows

package cloudfiles

import (
	"os"
	"syscall"
	"testing"
)

type attributeInfo struct {
	os.FileInfo
	attributes uint32
}

func (info attributeInfo) Sys() any {
	return &syscall.Win32FileAttributeData{FileAttributes: info.attributes}
}

func TestRecallMetadataIndependentOfProviderIntegration(t *testing.T) {
	for _, attrs := range []uint32{0x1000, 0x40000, 0x400000, 0x401000, 0x400010} {
		if !RecallsOnRead(attributeInfo{attributes: attrs}) {
			t.Errorf("unavailable attributes %x accepted", attrs)
		}
	}
	for _, attrs := range []uint32{0, 0x80, 0x200, 0x400, 0x10} {
		if RecallsOnRead(attributeInfo{attributes: attrs}) {
			t.Errorf("local attributes %x rejected", attrs)
		}
	}
	if RecallsOnRead(nil) {
		t.Fatal("nil metadata reported as an available provider observation")
	}
}
