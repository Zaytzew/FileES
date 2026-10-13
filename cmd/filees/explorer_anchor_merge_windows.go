//go:build windows && !nocfapi

package main

import (
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

// The same strict manifest/root binding as remote refresh and release. It
// supplies the already-known BASE, never a newer HEAD guessed during a copy.
func anchorMergeBoundRevision(record localrepo.Record) (int64, error) {
	guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
	if err != nil {
		return 0, err
	}
	defer closeAnchorReleaseGuards(guards)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(guards[len(guards)-1], &info); err != nil {
		return 0, err
	}
	tree, err := loadAnchorRemoteTree(record, anchorReleaseID(info))
	return tree.Revision, err
}
