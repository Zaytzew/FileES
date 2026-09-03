package main

import (
	"filees/pkg/clientprofile"
)

// serverPathSegment turns a server ID into something a filesystem will accept
// as part of a name.
//
// A server ID is the server's to choose and Unix stores almost anything, so
// `atmprojekt:filees` is an ordinary identifier - and unwritable on Windows,
// where a colon separates a drive letter and an alternate data stream. The
// encoding for that already exists in clientprofile.StateDirName: it touches
// only characters no platform permits, so any ID already in use keeps its
// spelling and nothing needs migrating.
//
// It is reused here rather than reimplemented, deliberately. The rule is only
// safe while there is exactly one of it; a second copy would drift, and one
// server would end up with two different names depending on which code path
// wrote the file.
//
// The reason this exists as its own function is that the sweep after r762
// declared the raw-concatenation bug fixed in "the only such place", and two
// more sites survived it - both building recovery-kit filenames. That sweep was
// done by reading. serverPathSegment gives the invariant something to point at,
// and TestNoPathIsBuiltFromARawServerID makes the next one mechanical.
func serverPathSegment(id string) (string, error) {
	return clientprofile.StateDirName(id)
}
