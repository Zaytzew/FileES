//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A working copy under FileES gets its own folder icon in Explorer, not an
// overlay badge.
//
// Overlays were the obvious route and are the wrong one. Windows keeps a
// single global table of overlay handlers, sorted alphabetically, with only
// fifteen usable slots; Dropbox, OneDrive and every other sync client register
// several each and the table has been full on ordinary machines for years. A
// product whose badge appears on some machines and not others has not built a
// feature, it has built a support case.
//
// The folder's own icon has none of that. It is the mechanism Windows itself
// uses for shell folders: a desktop.ini in the directory, and the directory
// marked so Explorer knows to read it. No shell extension, no registration, no
// administrator rights, and nothing to collide with.
//
// Measured on 2026-09-03 before writing any of this: a full svn add, commit
// and update cycle in a working copy carrying these attributes behaves exactly
// as one without them, and the attributes survive the cycle.
const managedFolderInfoTip = "Folder pod kuratelą FileES"

// markManagedFolder gives root the FileES folder icon, idempotently.
//
// Two facts make this safe in a working copy, and neither is obvious:
//
// desktop.ini is already in filepolicy.BuiltinIgnorePatterns, so the file this
// writes cannot be committed - which matters more here than anywhere, because
// it is written into a directory whose entire purpose is that its contents get
// versioned. It also means the icon is local rather than synchronised, which
// is correct: this is presentation, and every machine decides it for itself.
//
// READONLY on a directory does not mean read-only on Windows. It is the flag
// that tells Explorer the folder is customised; files inside are written,
// renamed and deleted exactly as before. Windows sets it on its own shell
// folders for the same reason. SYSTEM would work too and is deliberately not
// used: paired with HIDDEN it disappears under the default "hide protected
// operating system files", and a folder the owner cannot find is a worse
// outcome than a folder without a badge.
func markManagedFolder(root, iconPath string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(iconPath) {
		return errors.New("managed folder icon needs absolute paths")
	}
	if isVolumeRoot(root) {
		// A working copy at a drive root is not something to decorate. The
		// customisation would apply to the whole volume, and the desktop.ini
		// would sit where any tool scanning the drive will find it first.
		return errors.New("refusing to customise a volume root")
	}
	desktopINI := filepath.Join(root, "desktop.ini")
	want := encodeUTF16LE("[.ShellClassInfo]\r\n" +
		"IconResource=" + iconPath + ",0\r\n" +
		"InfoTip=" + managedFolderInfoTip + "\r\n")

	// Rewriting an identical file on every repository start would invalidate
	// the shell's icon cache for every managed folder on every daemon restart,
	// which is visible to the owner as folders flickering back to plain.
	existing, err := os.ReadFile(desktopINI)
	unchanged := err == nil && bytes.Equal(existing, want)
	if !unchanged {
		// The previous file carries HIDDEN|SYSTEM, and Windows refuses to open
		// such a file for writing without those flags being requested, so it is
		// cleared first rather than written through.
		if err == nil {
			if err := setFileAttributes(desktopINI, func(attrs uint32) uint32 {
				return attrs &^ (windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM)
			}); err != nil {
				return err
			}
		}
		if err := os.WriteFile(desktopINI, want, 0o644); err != nil {
			return err
		}
	}
	// Hidden and system so the file does not appear among the owner's drawings,
	// which is what Explorer does with its own.
	if err := setFileAttributes(desktopINI, func(attrs uint32) uint32 {
		return attrs | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM
	}); err != nil {
		return err
	}
	if err := setFileAttributes(root, func(attrs uint32) uint32 {
		return attrs | windows.FILE_ATTRIBUTE_READONLY
	}); err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	notifyShellFolderChanged(root)
	return nil
}

// unmarkManagedFolder returns root to an ordinary folder.
//
// A detached working copy is not under FileES any more and must not keep
// claiming to be. The owner's files stay exactly where they are; only the
// customisation goes.
func unmarkManagedFolder(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("managed folder icon needs an absolute path")
	}
	if isVolumeRoot(root) {
		// Symmetrical with marking: nothing was ever written there, so there
		// is nothing to remove - and clearing attributes on a whole volume
		// because a path was malformed is not a tidy-up, it is damage.
		return errors.New("refusing to modify a volume root")
	}
	desktopINI := filepath.Join(root, "desktop.ini")
	if _, err := os.Lstat(desktopINI); err == nil {
		if err := setFileAttributes(desktopINI, func(attrs uint32) uint32 {
			return attrs &^ (windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM)
		}); err != nil {
			return err
		}
		if err := os.Remove(desktopINI); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := setFileAttributes(root, func(attrs uint32) uint32 {
		return attrs &^ windows.FILE_ATTRIBUTE_READONLY
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	notifyShellFolderChanged(root)
	return nil
}

// isVolumeRoot reports whether path is the top of a drive or share, where
// filepath.Dir returns the path itself.
func isVolumeRoot(path string) bool {
	cleaned := filepath.Clean(path)
	return filepath.Dir(cleaned) == cleaned
}

func setFileAttributes(path string, mutate func(uint32) uint32) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(pathPtr)
	if err != nil {
		return err
	}
	next := mutate(attrs)
	if next == attrs {
		return nil
	}
	return windows.SetFileAttributes(pathPtr, next)
}

var (
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	procShChangeNoti = shell32.NewProc("SHChangeNotify")
)

const (
	shcneUpdateDir = 0x00001000
	shcnfPathW     = 0x0005
)

// notifyShellFolderChanged asks Explorer to re-read the folder.
//
// Without it the new icon appears whenever the shell next happens to refresh,
// which for a folder already on screen can be never. Best effort by design:
// nothing about a versioned working copy depends on the shell noticing, and a
// failure here must not fail a repository start.
func notifyShellFolderChanged(root string) {
	pathPtr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return
	}
	_, _, _ = procShChangeNoti.Call(uintptr(shcneUpdateDir), uintptr(shcnfPathW), uintptr(unsafe.Pointer(pathPtr)), 0)
}

// encodeUTF16LE writes the BOM-prefixed encoding Explorer expects.
//
// GetPrivateProfileString, which is how the shell reads desktop.ini, treats a
// file as Unicode only when it starts with the byte-order mark; without it a
// path or tooltip containing a non-ASCII character - a Polish name in the
// user's profile directory is enough - is read through the system codepage and
// silently mangled into a path that resolves to nothing.
func encodeUTF16LE(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, 2+2*len(units))
	out = append(out, 0xFF, 0xFE)
	var scratch [2]byte
	for _, unit := range units {
		binary.LittleEndian.PutUint16(scratch[:], unit)
		out = append(out, scratch[0], scratch[1])
	}
	return out
}
