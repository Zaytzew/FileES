package portablepath

import "strings"

// Collides reports which sibling the proposed name would become on a
// case-folding filesystem, or "" when none does. An exact match is not a
// collision: that is the same file, not two.
//
// The folding is uppercase comparison, and the choice is not cosmetic. Windows
// folds names with RtlUpcaseUnicodeString - it uppercases - so uppercasing is
// the model of the thing being predicted. Measured 2026-09-08 with Go 1.x:
//
//	para                 EqualFold   ToUpper==
//	tekst.txt/Tekst.txt  true        true
//	ı.txt/I.txt          false       true      <- Turkish dotless i
//	i.txt/İ.txt          false       false
//	straße.txt/STRASSE   false       false
//
// The Turkish row is the whole argument. strings.EqualFold uses Unicode simple
// folding, which leaves the dotless i alone; Windows uppercases it to I and the
// two names become one file. EqualFold would therefore have said "no collision"
// about a pair that certainly collides - an under-match, which is the direction
// that costs somebody a file. strings.ToLower has the same defect and is what
// concepts/PORTABLE_PATH_GATE_CONCEPT.md §8 warns against by name.
//
// The last two rows are not gaps: Windows does not fold those either, so
// refusing them would be a false alarm. This is a prediction about another
// system, so it is exactly as good as the model - and unlike the deletion guard
// in pkg/commit, over-matching is not free here. That guard only ever declines
// to delete, so matching too eagerly is safe; this one refuses to create, and a
// false refusal blocks work that is perfectly legal. Where the answer can be
// measured instead of predicted - a Windows client asking about its own
// directory - measurement is better, and pkg/commit does exactly that.
func Collides(name string, siblings []string) string {
	upper := strings.ToUpper(name)
	for _, sibling := range siblings {
		if sibling == name {
			continue
		}
		if strings.ToUpper(sibling) == upper {
			return sibling
		}
	}
	return ""
}
