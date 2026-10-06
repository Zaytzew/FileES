//go:build windows && !nocfapi

// Package cloudfiles recognises Windows Cloud Files placeholders, the files and
// folders an Explorer anchor shows before they are on this disk
// (native/filees-cfapi). Every part of FileES that walks a working copy must
// be able to tell them apart from the person's own files.
package cloudfiles

import (
	"bytes"
	"runtime"
	"sync"
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
	loadState     sync.Once
	stateProc     *windows.LazyProc
	modeProc      *windows.LazyProc
	infoProc      *windows.LazyProc
	localIdentity = []byte{'f', 0, 'i', 0, 'l', 0, 'e', 0, 'e', 0, 's', 0, '.', 0,
		'l', 0, 'o', 0, 'c', 0, 'a', 0, 'l', 0, '/', 0, 'v', 0, '1', 0, 0, 0}
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
			infoProc = cldapi.NewProc("CfGetPlaceholderInfo")
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
// fetched. It says nothing about publication: LocalState distinguishes retained
// local work from a seeded placeholder that has not been adopted into SVN.
// An anchor's own folder is a sync root, not a placeholder.
func IsPlaceholder(path string) bool {
	value, ok := state(path)
	return ok && value&statePlaceholder != 0 && value&stateSyncRoot == 0
}

// LocalState recognises only the helper's fully local, ALWAYS_FULL identity,
// not an arbitrary hydrated remote placeholder. Attribute-only inspection
// neither hydrates nor starts a subprocess. This marker survives rename;
// those local bytes remain ordinary work for the scanner/commit service.
func LocalState(path string) (managed, inSync bool) {
	if !IsPlaceholder(path) || NotOnDisk(path) || infoProc == nil || infoProc.Find() != nil {
		return false, false
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, false
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, false
	}
	defer windows.CloseHandle(handle)
	// CF_PLACEHOLDER_BASIC_INFO: two enums, two LARGE_INTEGERs, length/blob.
	var info struct {
		Pin, Sync      uint32
		FileID, RootID int64
		Length         uint32
		Identity       [4096]byte
	}
	var returned uint32
	hr, _, _ := infoProc.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&returned)))
	if int32(hr) < 0 || info.Length != uint32(len(localIdentity)) ||
		returned < uint32(unsafe.Offsetof(info.Identity))+info.Length || !bytes.Equal(info.Identity[:info.Length], localIdentity) {
		return false, false
	}
	return true, info.Sync == 1
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
