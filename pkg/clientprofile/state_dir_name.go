package clientprofile

import (
	"fmt"
	"os"
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
		// The escape character itself, so the mapping stays injective. Without
		// it the ID "a+3Ab" and the ID "a:b" would land in one directory and
		// two servers would quietly share a private key and a known_hosts -
		// which is not untidiness but a silent swap of identity.
		case r == escapeRune:
			b.WriteString(escape('+'))
		// Reserved by Windows. The forward and back slash are rejected by
		// validate() before this, since a separator in an ID is a different
		// kind of mistake.
		case strings.ContainsRune(`:*?"<>|`, r):
			b.WriteString(escape(r))
		case r < 0x20 || r == 0x7f:
			b.WriteString(escape(r))
		default:
			b.WriteRune(r)
		}
	}
	name := b.String()

	// Windows strips a trailing dot or space, so a name ending in one resolves
	// to a different directory than the one asked for.
	if last := name[len(name)-1]; last == '.' || last == ' ' {
		name = name[:len(name)-1] + escape(rune(last))
	}

	// Device names are reserved with or without an extension and regardless of
	// case, so CON, con and con.txt all fail. Encoding the first character is
	// enough to stop the match and keeps the rest readable.
	if isReservedDeviceName(name) {
		name = escape(rune(name[0])) + name[1:]
	}
	return name, nil
}

// escapeRune is '+' and was '%' until 2026-09-03.
//
// Percent is OpenSSH's own syntax. Every path FileES hands to ssh - the pinned
// known_hosts, the identity file - is percent-expanded by it, so a directory
// named atmprojekt%3Afilees made ssh refuse to start with "unknown key %3" and
// exit 255. Activation then failed in a way that named neither the path nor
// the encoding, and finding it cost a night.
//
// Plus is legal on every filesystem here, is not syntax for ssh, and is not a
// metacharacter for cmd.exe or PowerShell - unlike '&', which would have split
// a pasted path into two commands somewhere nobody was looking.
const escapeRune = '+'

func escape(r rune) string { return fmt.Sprintf("%c%02X", escapeRune, r) }

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
	root = filepath.Clean(root)
	if name != serverID {
		// An encoded name reads like a fault to anyone who meets it later, and
		// a plausible wrong explanation is worse than no explanation: the
		// reader stops looking. Today alone two names cost hours that way - a
		// mail state called "queued" that meant delivered, and a config error
		// that named the wrong cause. The rule that came out of it is that a
		// name which reads as a defect needs a sentence where it is read, and
		// the place this one is read is a file manager.
		writeEncodingNotice(root)
	}
	return filepath.Join(root, name), nil
}

const encodingNoticeName = "CZYTAJ-TO-nazwy-katalogow.txt"

const encodingNotice = `Nazwy katalogow w tym miejscu sa KODOWANE. To nie jest usterka.

Katalog nazywa sie tak samo jak identyfikator serwera - dopoki identyfikator
zawiera wylacznie znaki, ktore system plikow przyjmuje. Windows nie przyjmuje
miedzy innymi dwukropka, wiec serwer o identyfikatorze

    atmprojekt:filees

ma katalog

    atmprojekt+3Afilees

Znakiem ucieczki jest "+", po nim dwie cyfry szesnastkowe z kodem znaku
(3A to dwukropek, 2B to sam plus). Katalogi serwerow bez takich znakow - na
przyklad "spot" czy "manual" - nazywaja sie doslownie i nic ich nie dotyczy.

JESLI SZUKASZ PRZYCZYNY AWARII: to nie tutaj. Kodowanie jest zamierzone,
stosowane na wszystkich systemach jednakowo i sprawdzane testami. Aplikacja
wszedzie pokazuje prawdziwy identyfikator z dwukropkiem; zakodowana nazwa
wystepuje wylacznie jako nazwa katalogu na dysku.

Dlaczego "+", a nie "%": procent jest wlasna skladnia OpenSSH. Sciezki
podawane ssh sa przez niego rozwijane, wiec katalog "atmprojekt%3Afilees"
konczyl sie bledem "unknown key %3" i odmowa startu.
`

// writeEncodingNotice is best effort and deliberately silent. A profile
// directory must not fail to resolve because an explanatory file could not be
// written next to it.
func writeEncodingNotice(root string) {
	path := filepath.Join(root, encodingNoticeName)
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(encodingNotice), 0o600)
}
