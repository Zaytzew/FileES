//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

// hideFileesDir marks the top-level .filees directory FILE_ATTRIBUTE_HIDDEN.
// Unlike .svn, whose hidden attribute is set by the svn client itself on
// Windows, .filees is created directly by this daemon via os.MkdirAll,
// which never touches Windows file attributes - so it showed up as an
// ordinary visible folder in Explorer next to the (correctly hidden) .svn,
// even though both are equally private, dot-prefixed metadata directories.
// Idempotent and safe to call on every repo start, not just first creation.
func hideFileesDir(path string) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(pathPtr)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
		return nil
	}
	return windows.SetFileAttributes(pathPtr, attrs|windows.FILE_ATTRIBUTE_HIDDEN)
}
