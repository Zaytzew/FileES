package portablepath

import "testing"

func TestPlainCaseCollisionIsFound(t *testing.T) {
	got := Collides("tekst.txt", []string{"rysunek.dwg", "Tekst.txt", "umowa.pdf"})
	if got != "Tekst.txt" {
		t.Fatalf("Collides = %q, want Tekst.txt", got)
	}
}

// The same name is the same file. Reporting a collision here would refuse every
// ordinary save.
func TestAnExactMatchIsNotACollision(t *testing.T) {
	if got := Collides("tekst.txt", []string{"tekst.txt"}); got != "" {
		t.Fatalf("Collides = %q, want none", got)
	}
}

func TestDistinctNamesDoNotCollide(t *testing.T) {
	if got := Collides("tekst.txt", []string{"tekst1.txt", "tekst.txt.bak", "teksty.txt"}); got != "" {
		t.Fatalf("Collides = %q, want none", got)
	}
}

// The regression that fixes the folding model. Windows uppercases, so the
// Turkish dotless i becomes I and these two names become one file.
// strings.EqualFold - and strings.ToLower - both say these are different, which
// would let the collision through. An under-match here costs somebody a file.
func TestTurkishDotlessIStillCollides(t *testing.T) {
	if got := Collides("ı.txt", []string{"I.txt"}); got != "I.txt" {
		t.Fatalf("Collides = %q, want I.txt: uppercase folding is what Windows does", got)
	}
}

// Windows does not fold these, so neither may we. Refusing them would be a
// false alarm, and a gate people stop trusting is worse than no gate.
func TestPairsWindowsDoesNotFoldAreAllowed(t *testing.T) {
	for _, pair := range [][2]string{
		{"straße.txt", "STRASSE.txt"},
		{"i.txt", "İ.txt"},
	} {
		if got := Collides(pair[0], []string{pair[1]}); got != "" {
			t.Errorf("Collides(%q, %q) = %q, want none", pair[0], pair[1], got)
		}
	}
}

func TestCollisionIgnoresExtensionBoundaries(t *testing.T) {
	if got := Collides("Rysunek.DWG", []string{"rysunek.dwg"}); got != "rysunek.dwg" {
		t.Fatalf("Collides = %q, want rysunek.dwg", got)
	}
}
