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
// folding, which leaves the dotless i alone; uppercase maps it to I and the
// model treats the two names alike. EqualFold would have said "no collision"
// about a pair that collides in that uppercase model. An under-match is the
// direction that can cost somebody a file. strings.ToLower has the same defect and is what
// implementation notes (not distributed) §8 warns against by name.
//
// The last two rows are not gaps in the portable model, so
// refusing them would be a false alarm. This is a prediction about another
// system, so it is exactly as good as the model - and unlike the deletion guard
// in pkg/commit, over-matching is not free here. That guard only ever declines
// to delete, so matching too eagerly is safe; this one refuses to create, and a
// false refusal blocks work that is perfectly legal. Where the answer can be
// measured instead of predicted - a Windows client asking about its own
// directory - measurement is better, and pkg/commit does exactly that.
// Native CREATE_NEW on this station's C: (2026-10-10) accepted both dotless
// i and I as distinct files. On-volume NTFS folding is not universal Win32/Go
// uppercase; this portable gate is conservative, never proof of a FileID alias.
func Collides(name string, siblings []string) string {
	upper := CollisionKey(name)
	for _, sibling := range siblings {
		if sibling == name {
			continue
		}
		if CollisionKey(sibling) == upper {
			return sibling
		}
	}
	return ""
}

// CollisionKey shares the uppercase model above with bounded path indexes.
// Unlike Collides, equal keys also identify an exact duplicate in a list.
func CollisionKey(name string) string { return strings.ToUpper(name) }
