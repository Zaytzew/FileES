// Package portablepath judges whether a name can exist, unchanged, on every
// platform FileES supports.
//
// Two callers need this knowledge for opposite reasons, which is why it lives
// in one place. clientprofile *produces* a safe name by escaping what Windows
// reserves; the portable path gate *judges* a name the user chose and refuses
// it. Two copies of the table would drift, and the drift would show up as one
// rule at some call sites and not others - which is not a rule.
//
// The rules are Windows' because Windows is the strictest of the three, but
// they are applied on every platform on purpose. Deciding per-OS would let a
// Linux client create a name that no Windows client can ever check out, which
// is precisely the failure this package exists to stop.
//
// See concepts/PORTABLE_PATH_GATE_CONCEPT.md for why the whole class is one
// rule rather than four patches.
package portablepath

import (
	"fmt"
	"strings"
)

// Kind names why a segment cannot be represented. It is deliberately not a
// message: the wording belongs to whoever shows it, the same way errcat hints
// are presentational and drive nothing.
type Kind int

const (
	// ReservedDevice - CON, LPT1 and friends, with or without an extension.
	ReservedDevice Kind = iota + 1
	// ReservedRune - a character Win32 forbids in a name.
	ReservedRune
	// ControlRune - C0 or DEL.
	ControlRune
	// Separator - a path separator inside what should be a single name.
	Separator
	// TrailingDotOrSpace - Windows silently strips these, so the name that
	// gets created is not the name that was asked for.
	TrailingDotOrSpace
	// Empty - no name at all.
	Empty
)

// Problem describes one reason a segment is unrepresentable. Detail carries the
// offending character where there is one, so a caller can quote it.
type Problem struct {
	Kind   Kind
	Detail string
}

func (p Problem) String() string {
	switch p.Kind {
	case ReservedDevice:
		return "nazwa jest zarezerwowana przez system dla urządzenia"
	case ReservedRune:
		return fmt.Sprintf("znak %q jest niedozwolony w nazwie", p.Detail)
	case ControlRune:
		return "nazwa zawiera znak sterujący"
	case Separator:
		return fmt.Sprintf("znak %q rozdziela ścieżkę i nie może być częścią nazwy", p.Detail)
	case TrailingDotOrSpace:
		return fmt.Sprintf("nazwa kończy się znakiem %q, który zostaje po cichu usunięty", p.Detail)
	case Empty:
		return "nazwa jest pusta"
	}
	return "nazwa jest nieprzedstawialna"
}

// reservedRunes is the Win32 set. The separators are handled separately,
// because a separator inside a name is a different mistake from a forbidden
// character, and clientprofile rejects those earlier for its own reasons.
const reservedRunes = `:*?"<>|`

var reservedDeviceNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// IsReservedRune reports whether Win32 forbids r in a name. Separators are not
// included; see reservedRunes.
func IsReservedRune(r rune) bool { return strings.ContainsRune(reservedRunes, r) }

// IsControlRune reports whether r is C0 or DEL.
func IsControlRune(r rune) bool { return r < 0x20 || r == 0x7f }

// IsReservedDeviceName reports whether name matches a reserved device. The
// match ignores case and any extension, so CON, con and con.txt all do.
func IsReservedDeviceName(name string) bool {
	stem := name
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	_, reserved := reservedDeviceNames[strings.ToUpper(stem)]
	return reserved
}

// SegmentProblem reports why one path segment cannot be represented, or nil
// when it can. It judges the name alone; a name that is fine in isolation can
// still collide with a sibling, which is Collision's question.
func SegmentProblem(segment string) *Problem {
	if segment == "" {
		return &Problem{Kind: Empty}
	}
	for _, r := range segment {
		switch {
		case r == '/' || r == '\\':
			return &Problem{Kind: Separator, Detail: string(r)}
		case IsControlRune(r):
			return &Problem{Kind: ControlRune}
		case IsReservedRune(r):
			return &Problem{Kind: ReservedRune, Detail: string(r)}
		}
	}
	if last := segment[len(segment)-1]; last == '.' || last == ' ' {
		return &Problem{Kind: TrailingDotOrSpace, Detail: string(last)}
	}
	if IsReservedDeviceName(segment) {
		return &Problem{Kind: ReservedDevice}
	}
	return nil
}
