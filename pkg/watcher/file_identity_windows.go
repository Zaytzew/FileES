package watcher

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// fileIdentity is the Windows counterpart of the Linux statx identity: the
// volume serial number and file index stand in for device and inode, and the
// creation time for btime.
//
// It exists because without it the whole native lane is unreachable. Measured
// 2026-09-08 on a live Windows daemon
// (reports/NATIVE_SVN_WINDOWS_DAEMON_LAB_2026-09-08.md, NATIVE-WIN-IDENTITY):
// the !linux build returned an empty string, RequireRenameIdentity is on
// whenever the native helper is, so every rename became RenameUncertain and
// commit refused before the C helper was ever called. record-move - the one
// verb the helper was written for - could not be reached from a real client.
//
// The same three guards as Linux, for the same reasons: a directory or reparse
// point is not a file whose ancestry we can follow, a hard-linked file has no
// single identity to follow, and without a creation time there is no evidence
// at all. MD5 is not a substitute here; that is precisely what native mode
// refuses to accept as ancestry.
func fileIdentity(path string) string {
	pathPtr, err := windows.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return ""
	}
	// FILE_READ_ATTRIBUTES with every share bit set is deliberate. The files
	// this product watches are open in AutoCAD and Word for hours at a time,
	// and an identity that can only be read when nobody is editing would be
	// absent exactly when a rename is most likely. Attributes need no read
	// access, so this neither blocks the editor nor fails on it.
	// OPEN_REPARSE_POINT keeps us from silently identifying the target of a
	// link instead of the link.
	handle, err := windows.CreateFile(pathPtr,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return ""
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return ""
	}
	if info.NumberOfLinks != 1 {
		return ""
	}
	created := info.CreationTime.Nanoseconds()
	if created <= 0 {
		return ""
	}
	index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	if index == 0 {
		// Some filesystems report no index at all. An identity that is the
		// same for every file is worse than none: it would make two unrelated
		// files look like one object moving.
		return ""
	}
	return fmt.Sprintf("windows:%d:%d:%d", info.VolumeSerialNumber, index, created)
}

// extendedPath prefixes \\?\ so a path past MAX_PATH can still be opened. The
// working copies this watches are real project trees, and one measured on this
// machine was 337 characters.
func extendedPath(path string) string {
	if len(path) < 240 || strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	if len(path) > 2 && path[1] == ':' {
		return `\\?\` + path
	}
	return path
}
