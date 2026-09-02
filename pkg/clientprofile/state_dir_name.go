package clientprofile

import (
	"fmt"
	"path/filepath"
	"strings"
)

// stateDirName turns a server ID into a directory name that every supported
// platform will accept.
//
// The ID and the directory had been the same string, which works until an ID
// contains something the filesystem reserves. On Windows a colon separates a
// drive letter and an alternate data stream, so `atmprojekt:filees` - a
// perfectly ordinary identifier that Unix stores without complaint - failed
// activation with "The directory name is invalid" at the first mkdir. The
// identifier is the server's to choose; making the filesystem's rules part of
// that choice would push a Windows limitation onto everyone.
//
// It is identity for anything already in use: only characters no platform
// permits are touched, so existing state directories keep their names and
// nothing has to be migrated.
//
// The same encoding runs on every platform on purpose. Deciding per-OS would
// give one server two different directories depending on where its client
// runs, and this product moves working copies between machines.
// StateDirName is exported because two packages must agree on it: deploy
// creates the directory and this package enumerates it, and the loader asserts
// the name matches the ID it finds inside. Two copies of this rule would drift.
func StateDirName(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("server profile ID is empty")
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		// Percent itself, so the mapping stays injective. Without it the ID
		// "a%3Ab" and the ID "a:b" would land in one directory and two servers
		// would quietly share state - a worse outcome than the rename this
		// costs an installation that already used a percent sign.
		case r == '%':
			b.WriteString("%25")
		// Reserved by Windows. The forward and back slash are rejected by
		// validate() before this, since a separator in an ID is a different
		// kind of mistake.
		case strings.ContainsRune(`:*?"<>|`, r):
			fmt.Fprintf(&b, "%%%02X", r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "%%%02X", r)
		default:
			b.WriteRune(r)
		}
	}
	name := b.String()

	// Windows strips a trailing dot or space, so a name ending in one resolves
	// to a different directory than the one asked for.
	if last := name[len(name)-1]; last == '.' || last == ' ' {
		name = name[:len(name)-1] + fmt.Sprintf("%%%02X", last)
	}

	// Device names are reserved with or without an extension and regardless of
	// case, so CON, con and con.txt all fail. Encoding the first character is
	// enough to stop the match and keeps the rest readable.
	if isReservedDeviceName(name) {
		name = fmt.Sprintf("%%%02X", name[0]) + name[1:]
	}
	return name, nil
}

var reservedDeviceNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

func isReservedDeviceName(name string) bool {
	stem := name
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	_, reserved := reservedDeviceNames[strings.ToUpper(stem)]
	return reserved
}

// ServerDir is the directory holding one server's client state.
//
// It exists because the join was written out at six call sites and only one of
// them learned about encoding, which is how activation got past mkdir and then
// failed opening known_hosts under the name that could not exist. A rule
// applied at some call sites and not others is not a rule.
func ServerDir(root, serverID string) (string, error) {
	name, err := StateDirName(serverID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(root), name), nil
}
