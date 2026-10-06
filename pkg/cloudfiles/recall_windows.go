//go:build windows

package cloudfiles

import (
	"os"
	"syscall"
)

// OFFLINE, RECALL_ON_OPEN and RECALL_ON_DATA_ACCESS remain visible even when
// Windows disguises a provider's placeholder as an ordinary file. Inspection
// uses only the caller's metadata, never reads content or contacts a provider.
const recallAttributes = 0x00001000 | 0x00040000 | 0x00400000

// RecallsOnRead reports that content may require its provider to supply bytes.
// This defensive check also belongs in Store/nocfapi builds: disabling FileES
// virtual folders does not make another provider's missing bytes local.
func RecallsOnRead(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&recallAttributes != 0
}
