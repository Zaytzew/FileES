//go:build windows && !nocfapi

// Package cloudfiles recognises Windows Cloud Files placeholders, the files and
// folders an Explorer anchor shows before they are on this disk
// (native/filees-cfapi). Every part of FileES that walks a working copy must
// be able to tell them apart from the person's own files.
package cloudfiles

import (
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CF_PLACEHOLDER_STATE bits (cfapi.h).
const (
	statePlaceholder = 0x00000001
	stateSyncRoot    = 0x00000002
	statePartial     = 0x00000010
	stateInvalid     = 0xFFFFFFFF
)

// PHCM_EXPOSE_PLACEHOLDERS: RtlSetThreadPlaceholderCompatibilityMode.
const exposePlaceholders = 2

var (
	loadState sync.Once
	stateProc *windows.LazyProc
	modeProc  *windows.LazyProc
)

// state asks the system, not the reparse tag: the anchor's own root carries
// the same tag as its placeholders, and a raw tag check cannot tell a folder
// that is an anchor from one that is not on this disk yet.
//
// Windows disguises placeholders from programs that do not declare they know
// about them: a placeholder folder then looks like an ordinary folder and a
// placeholder file differs from one only by an attribute. Measured 2026-09-23:
// disguised, the placeholder state of both reads as 0. The check therefore
// exposes placeholders for this one thread, for the length of one lookup, and
// puts the thread back. A process-wide switch would change how the whole
// daemon sees every file (reparse points where it expects plain ones).
func state(path string) (uint32, bool) {
	loadState.Do(func() {
		cldapi := windows.NewLazySystemDLL("cldapi.dll")
		if cldapi.Load() == nil {
			stateProc = cldapi.NewProc("CfGetPlaceholderStateFromFindData")
		}
		modeProc = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlSetThreadPlaceholderCompatibilityMode")
	})
	if stateProc == nil || stateProc.Find() != nil || modeProc.Find() != nil {
		return 0, false
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous, _, _ := modeProc.Call(exposePlaceholders)
	defer modeProc.Call(uintptr(uint8(previous)))
	var found windows.Win32finddata
	handle, err := windows.FindFirstFile(name, &found)
	if err != nil {
		return 0, false
	}
	_ = windows.FindClose(handle)
	result, _, _ := stateProc.Call(uintptr(unsafe.Pointer(&found)))
	value := uint32(result)
	if value == stateInvalid {
		return 0, false
	}
	return value, true
}

// IsPlaceholder reports whether path is a Cloud Files placeholder that has not
// been turned back into an ordinary file - whether or not its bytes have been
// fetched. Any provider's placeholder counts: none of them is work the person
// produced, and a fetched one is about to be taken into the working copy.
// An anchor's own folder is a sync root, not a placeholder.
func IsPlaceholder(path string) bool {
	value, ok := state(path)
	return ok && value&statePlaceholder != 0 && value&stateSyncRoot == 0
}

// NotOnDisk reports whether path is a placeholder whose content is not (or not
// entirely) on this disk: reading it would download it.
func NotOnDisk(path string) bool {
	value, ok := state(path)
	return ok && value&statePlaceholder != 0 && value&statePartial != 0
}

// IsSyncRoot reports whether path is the root of an anchor or of another
// provider's synchronised folder.
func IsSyncRoot(path string) bool {
	value, ok := state(path)
	return ok && value&stateSyncRoot != 0
}

// FILE_ATTRIBUTE_RECALL_ON_OPEN and FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS.
// Unlike the placeholder state, these stay visible when Windows disguises a
// placeholder, and they arrive with the directory listing for free.
const recallAttributes = 0x00040000 | 0x00400000

// RecallsOnRead reports, from a FileInfo the caller already has, whether
// reading the file would download it: a placeholder of an Explorer anchor or
// of any other provider (Nextcloud, OneDrive) whose bytes are not on this
// disk. Costs no system call, so a scanner can ask it for every file.
func RecallsOnRead(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&recallAttributes != 0
}
